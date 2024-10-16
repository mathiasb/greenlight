package main

import (
	"errors"
	"expvar"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mathiasb/greenlight/internal/data"
	"github.com/mathiasb/greenlight/internal/validator"
	"github.com/tomasen/realip"
	"golang.org/x/time/rate"
)

func (app *application) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				w.Header().Set("Connection", "close")
				app.serverErrorResponse(w, r, fmt.Errorf("%s", err))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

func (app *application) rateLimit(next http.Handler) http.Handler {
	type client struct {
		limiter  *rate.Limiter
		lastSeen time.Time
	}
	var (
		mu      = sync.Mutex{}
		clients = make(map[string]*client)
	)
	go func() {
		for {
			time.Sleep(time.Minute)
			mu.Lock()
			for ip, client := range clients {
				if time.Since(client.lastSeen) > 3*time.Minute {
					delete(clients, ip)
				}
			}
			mu.Unlock()
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.config.limiter.enabled {
			ip := realip.FromRequest(r)

			mu.Lock()
			if _, found := clients[ip]; !found {
				clients[ip] = &client{
					limiter: rate.NewLimiter(
						rate.Limit(app.config.limiter.rps),
						app.config.limiter.burst)}
			}
			clients[ip].lastSeen = time.Now()

			if !clients[ip].limiter.Allow() {
				mu.Unlock()
				app.rateLimitExceededResponse(w, r)
				return
			}
			mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

/***
** User authenticaton and persmissions
***/

func (app *application) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Authorization")
			authorizationHeader := r.Header.Get("Authorization")
			if authorizationHeader == "" {
				r = app.contextSetUser(r, data.AnonymousUser)
				next.ServeHTTP(w, r)
				return
			}

			headerParts := strings.Split(authorizationHeader, " ")
			if len(headerParts) != 2 || headerParts[0] != "Bearer" {
				app.invalidAuthenticationTokenResponse(w, r)
				return
			}

			token := headerParts[1]
			v := validator.New()
			if data.ValidateTokenPlaintext(v, token); !v.Valid() {
				app.invalidAuthenticationTokenResponse(w, r)
				return
			}

			user, err := app.models.Users.GetForToken(data.ScopeAuthentication, token)
			if err != nil {
				switch {
				case errors.Is(err, data.ErrRecordNotFound):
					app.invalidAuthenticationTokenResponse(w, r)
				default:
					app.serverErrorResponse(w, r, err)
				}
				return
			}

			r = app.contextSetUser(r, user)
			next.ServeHTTP(w, r)
		},
	)
}

func (app *application) requireAuthenticatedUser(next http.HandlerFunc) http.HandlerFunc {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			user := app.contextGetUser(r)
			if user.IsAnonymous() {
				app.authenticationRequiredResponse(w, r)
				return
			}

			next.ServeHTTP(w, r)
		},
	)
}

func (app *application) requireActivatedUser(next http.HandlerFunc) http.HandlerFunc {
	return app.requireAuthenticatedUser(
		http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				user := app.contextGetUser(r)
				if !user.Activated {
					app.inactiveAccountResponse(w, r)
					return
				}
				next.ServeHTTP(w, r)
			},
		),
	)
}

func (app *application) requirePermission(code string, next http.HandlerFunc) http.HandlerFunc {
	fn := func(w http.ResponseWriter, r *http.Request) {
		user := app.contextGetUser(r)
		permissions, err := app.models.Permissions.GetAllForUser(user.ID)
		if err != nil {
			app.serverErrorResponse(w, r, err)
			return
		}

		if !permissions.Include(code) {
			app.notPermittedResponse(w, r)
			return
		}

		next.ServeHTTP(w, r)
	}
	return app.requireActivatedUser(fn)
}

func (app *application) enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Origin")
			w.Header().Add("Vart", "Access-Control-Request-Method")
			origin := r.Header.Get("Origin")

			if origin != "" {
				for _, o := range app.config.cors.trustedOrigins {
					if origin == o {
						w.Header().Set("Access-Control-Allow-Origin", origin)
						// Check for pre-flight reqest
						if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
							// Set pre-flight response headers
							w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, PUT, PATCH, DELETE")
							w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
							// Write the header and return, stopping the middleware chain
							// https://stackoverflow.com/questions/46026409/what-are-proper-status-codes-for-cors-preflight-requests/58794243#58794243
							w.WriteHeader(http.StatusOK)
							return
						}

						break
					}
				}
			}

			next.ServeHTTP(w, r)
		},
	)
}

/***
** Metrics
***/

type metricsResponseWriter struct {
	http.ResponseWriter // embed the ResponseWriter with its methods
	statusCode          int
	headerWritten       bool
}

func newMetricsResponseWriter(w http.ResponseWriter) *metricsResponseWriter {
	return &metricsResponseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}
}

func (mw *metricsResponseWriter) WriteHeader(statusCode int) {
	mw.statusCode = statusCode
	mw.ResponseWriter.WriteHeader(statusCode)
}

func (mw *metricsResponseWriter) Write(b []byte) (int, error) {
	if !mw.headerWritten {
		mw.headerWritten = true
		mw.ResponseWriter.WriteHeader(http.StatusOK)
	}
	return mw.ResponseWriter.Write(b)
}

func (mw *metricsResponseWriter) Unwrap() http.ResponseWriter {
	return mw.ResponseWriter
}

func (app *application) metrics(next http.Handler) http.Handler {
	var (
		totalRequestsReceived      = expvar.NewInt("total_requests_received")
		totalResponsesSent         = expvar.NewInt("total_responses_sent")
		totalResponsesSentByStatus = expvar.NewMap("total_responses_sent_by_status")
		totalProcessingTimeMicros  = expvar.NewInt("total_processing_time_micros")
	)
	return http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			totalRequestsReceived.Add(1)
			mw := newMetricsResponseWriter(w)

			next.ServeHTTP(mw, r)
			totalResponsesSent.Add(1)
			totalResponsesSentByStatus.Add(
				strconv.Itoa(mw.statusCode),
				1,
			)

			duration := time.Since(start).Microseconds()
			totalProcessingTimeMicros.Add(duration)
		},
	)
}

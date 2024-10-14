package main

import (
	"context"
	"net/http"

	"github.com/mathiasb/greenlight/internal/data"
)

type contextKey string

const (
	contextKeyUser = contextKey("user")
)

func (app *application) contextSetUser(r *http.Request, user *data.User) *http.Request {
	ctx := context.WithValue(r.Context(), contextKeyUser, user)
	return r.WithContext(ctx)
}

func (app *application) contextGetUser(r *http.Request) *data.User {
	user, ok := r.Context().Value(contextKeyUser).(*data.User)
	if !ok {
		panic("missing user value in request context")
	}
	return user
}

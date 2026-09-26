// Package middleware wraps the API's handlers: Auth verifies the bearer token
// and puts the identity in the request context, CORS admits the one configured
// origin. That identity is the tenant boundary the README's "Tenancy" section
// describes, so nothing below this package reads a user id from a header.
package middleware

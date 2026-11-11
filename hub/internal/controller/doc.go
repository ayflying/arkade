// Package controller defines shared response errors and authentication controllers.
//
// Native controllers implement (ctx, *Req) (*Res, error); g.Meta declares routes.
// API contracts live in api/<domain>/v1. Independent business controllers live in
// this package's users, groups and audit subpackages so parallel work does not
// require editing one central controller file.
//
// Controllers return typed values and safe business errors. The httpx response
// policy preserves Arkade's bare JSON contract instead of enabling GoFrame's
// default response envelope. SessionAccessor isolates the exceptional cookie
// operations; ErrNoContent represents a 204 response without a body.
package controller

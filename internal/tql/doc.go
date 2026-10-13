// SPDX-License-Identifier: Apache-2.0

// Package tql implements the Tidemark Query Language (SPEC §10): a lexer and
// recursive-descent parser producing an AST, a semantic checker, dashboard
// variable substitution, a compiler to parameterised ClickHouse SQL, and
// syntactic autocomplete.
//
// Typical use:
//
//	q, err := tql.Parse(text)                  // syntax; falls back to free text (§10.6)
//	q, err = tql.Substitute(q, vars)            // dashboards only (§13.1)
//	c, err := tql.Compile(q, tql.Options{From: from, To: to, Database: "tidemark"})
//	rows, err := conn.Query(ctx, c.SQL, c.Args...)
//
// Query errors are *Error values carrying a byte position, length, message
// and optional hint. Option errors wrap ErrInvalidOptions.
//
// Safety: SQL text is assembled only from constant fragments and identifiers
// from the built-in allow-list (or validated option values: the database name
// and indexed-field columns f_<slug>). Every user-supplied value, including
// property keys, is passed as a positional `?` argument.
package tql

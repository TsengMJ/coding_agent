package main

import (
	"context"
	"errors"
	"io"
)

var errPending = errors.New("not implemented yet")

func runGate(context.Context, env, []string, io.Writer, io.Writer) error    { return errPending }
func runRun(context.Context, env, []string, io.Writer, io.Writer) error     { return errPending }
func runGrade(context.Context, env, []string, io.Writer, io.Writer) error   { return errPending }
func runReport(context.Context, env, []string, io.Writer, io.Writer) error  { return errPending }
func runCompare(context.Context, env, []string, io.Writer, io.Writer) error { return errPending }
func runHistory(context.Context, env, []string, io.Writer, io.Writer) error { return errPending }
func runRefs(context.Context, env, []string, io.Writer, io.Writer) error    { return errPending }

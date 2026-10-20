package console

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/truvity/sluis/internal/consolecall"
	"github.com/truvity/sluis/internal/modcall"
)

// What a handler says crosses the wire as data when it is the answer to the
// question asked, as a refusal when it is a refusal, and as a bare code when the
// module itself failed.
func TestWhatAHandlerReturnedCrossesAsDataARefusalOrACode(t *testing.T) {
	c := Static(Deps{})
	ctx := context.Background()

	var problem *consolecall.Problem
	err := c.told(ctx, "m", connect.NewError(connect.CodeNotFound, "no such channel"))
	if !errors.As(err, &problem) || problem.Summary != "no such channel" || CodeOf(problem) != connect.CodeNotFound {
		t.Errorf("not found = %v", err)
	}
	if CodeOf(&consolecall.Problem{Summary: "x"}) != connect.CodeFailedPrecondition || CodeOf(nil) != connect.CodeFailedPrecondition {
		t.Error("a Problem naming no code is a failed precondition")
	}

	var coded *modcall.Error
	for code, want := range map[connect.Code]string{
		connect.CodePermissionDenied: modcall.CodeForbidden, connect.CodeUnauthenticated: "unauthenticated",
	} {
		if err = c.told(ctx, "m", connect.NewError(code, "this needs the operator role")); !errors.As(err, &coded) ||
			coded.Code != want || coded.Message != "this needs the operator role" {
			t.Errorf("%v = %v", code, err)
		}
	}
	for _, internal := range []error{connect.NewError(connect.CodeInternal, "store password is hunter2"), errors.New("hunter2")} {
		err = c.told(ctx, "m", internal)
		if !errors.As(err, &coded) || coded.Code != modcall.CodeInternal || coded.Message != "" || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("an internal failure told as %v", err)
		}
	}
	if c.told(ctx, "m", nil) != nil {
		t.Error("success is told as an error")
	}
}

func TestEveryRegisteredMethodAnswersTheConsoleAlone(t *testing.T) {
	s := NewServer(Static(Deps{}))
	if v := consolecall.Violations(s); len(v) != 0 {
		t.Fatal(v)
	}
	if len(s.Methods()) == 0 {
		t.Fatal("no method is registered")
	}
}

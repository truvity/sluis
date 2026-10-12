package main

import (
	"errors"
	"slices"
	"testing"
)

func TestVerifyTakesTheIDAndTheIdentityOutOfTheArguments(t *testing.T) {
	for _, args := range [][]string{
		{"20261013T020000Z-ab12cd", "--identity", "key.txt", "--config", "c.yaml"},
		{"--config", "c.yaml", "--identity=key.txt", "20261013T020000Z-ab12cd"},
	} {
		id, identity, rest, err := verifyArgs(args)
		if err != nil || id != "20261013T020000Z-ab12cd" || identity != "key.txt" || !slices.Equal(rest, []string{"--config", "c.yaml"}) {
			t.Errorf("%v: %q %q %v %v", args, id, identity, rest, err)
		}
	}
	if _, _, _, err := verifyArgs([]string{"--config", "c.yaml"}); !errors.Is(err, errUsage) {
		t.Errorf("no id: %v", err)
	}
}

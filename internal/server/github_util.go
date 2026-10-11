package server

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestampOf is a time for the page, absent when it never happened.
func timestampOf(at time.Time) *timestamppb.Timestamp {
	if at.IsZero() {
		return nil
	}
	return timestamppb.New(at)
}

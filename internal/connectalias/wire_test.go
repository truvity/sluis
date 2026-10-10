package connectalias_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	_ "github.com/truvity/sluis/gen/accessissuer/v1"
	_ "github.com/truvity/sluis/gen/directoryroster/v1"
	_ "github.com/truvity/sluis/gen/sluis/v1"
	"github.com/truvity/sluis/internal/connectalias"
)

// legacy is the package a sluis.v1 file was copied from: the issuer's
// session file, and the hub's operator contract for the rest.
func legacy(file string) string {
	if file == "session.proto" {
		return "accessissuer/v1/"
	}

	return "directoryroster/v1/"
}

// The messages and services of sluis.v1 are the legacy ones, unchanged.
// The wire carries field numbers and the path carries names, so a file
// with its package renamed in every type reference must equal its
// counterpart exactly: any field, number, type, enum value, service or
// method that differs fails here. This is the proof the path rewrite in
// this package depends on.
func TestSluisV1IsTheLegacyPackagesUnchanged(t *testing.T) {
	t.Parallel()

	renamer := strings.NewReplacer(".directoryroster.v1.", ".sluis.v1.", ".accessissuer.v1.", ".sluis.v1.")
	checked := 0

	protoregistry.GlobalFiles.RangeFilesByPackage("sluis.v1", func(current protoreflect.FileDescriptor) bool {
		file := strings.TrimPrefix(current.Path(), "sluis/v1/")
		old, err := protoregistry.GlobalFiles.FindFileByPath(legacy(file) + file)
		if err != nil {
			t.Errorf("%s has no legacy counterpart: %v", current.Path(), err)

			return true
		}

		want := protodesc.ToFileDescriptorProto(old)
		got := protodesc.ToFileDescriptorProto(current)
		normalise(want, renamer)
		normalise(got, renamer)

		if !proto.Equal(want, got) {
			t.Errorf("%s differs from %s:\nsluis.v1:\n%v\nlegacy, renamed:\n%v", current.Path(), old.Path(), got, want)
		}
		checked++

		return true
	})

	// Each of the 11 legacy files has its twin; a package that lost one
	// would pass the loop above without being looked at.
	if checked != 11 {
		t.Errorf("compared %d files, want 11", checked)
	}
}

// normalise puts a legacy file in the new package's terms and drops what
// is not the wire: where the file lives, its options and its comments.
func normalise(f *descriptorpb.FileDescriptorProto, r *strings.Replacer) {
	f.Name = nil
	f.Package = nil
	f.Options = nil
	f.SourceCodeInfo = nil
	for i, dep := range f.Dependency {
		f.Dependency[i] = strings.NewReplacer("directoryroster/v1/", "sluis/v1/").Replace(dep)
	}

	var message func(*descriptorpb.DescriptorProto)
	message = func(m *descriptorpb.DescriptorProto) {
		for _, field := range m.Field {
			if field.TypeName != nil {
				field.TypeName = proto.String(r.Replace(field.GetTypeName()))
			}
		}
		for _, nested := range m.NestedType {
			message(nested)
		}
	}
	for _, m := range f.MessageType {
		message(m)
	}
	for _, s := range f.Service {
		for _, m := range s.Method {
			m.InputType = proto.String(r.Replace(m.GetInputType()))
			m.OutputType = proto.String(r.Replace(m.GetOutputType()))
		}
	}
}

// A request addressed to the new name reaches the handler as the old one,
// with its method, query, headers and body as they were sent; anything
// else is not this service's.
func TestRewriteChangesTheServiceAndNothingElse(t *testing.T) {
	t.Parallel()

	var seen *http.Request
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(http.StatusAccepted)
	})
	handler := connectalias.Rewrite(next, "sluis.v1.Thing", "old.v1.Thing")

	request := httptest.NewRequest(http.MethodPost, "/sluis.v1.Thing/Do?encoding=json", strings.NewReader("{}"))
	request.Header.Set("X-Probe", "kept")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted || seen == nil {
		t.Fatalf("the handler was not reached: %d", recorder.Code)
	}
	if seen.URL.Path != "/old.v1.Thing/Do" || seen.URL.RawQuery != "encoding=json" ||
		seen.Method != http.MethodPost || seen.Header.Get("X-Probe") != "kept" {
		t.Errorf("rewritten request = %s %s?%s %v", seen.Method, seen.URL.Path, seen.URL.RawQuery, seen.Header)
	}
	if request.URL.Path != "/sluis.v1.Thing/Do" {
		t.Errorf("the caller's own request was changed: %s", request.URL.Path)
	}

	other := httptest.NewRecorder()
	handler.ServeHTTP(other, httptest.NewRequest(http.MethodPost, "/sluis.v1.Other/Do", nil))
	if other.Code != http.StatusNotFound {
		t.Errorf("another service = %d, want 404", other.Code)
	}
}

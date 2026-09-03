package mediaimport

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (fn resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return fn(ctx, network, host)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func publicResolver(_ context.Context, _ string, _ string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}

func TestFetchValidatesImageAndDoesNotForwardCredentials(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9WlD3p8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	importer := Importer{
		Resolver: resolverFunc(publicResolver),
		newClient: func(_ time.Duration, _ Resolver) *http.Client {
			return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Referer") != "" {
					t.Fatalf("source request carried a caller credential: %#v", request.Header)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(png))), ContentLength: int64(len(png)), Request: request}, nil
			})}
		},
	}

	result, err := importer.Fetch(context.Background(), "https://images.example/photo.png")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if result.MimeType != "image/png" || result.Host != "images.example" || len(result.Data) != len(png) {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFetchRejectsNonHTTPSAndOversizedOrNonImageResponses(t *testing.T) {
	t.Run("http is silently upgraded to https", func(t *testing.T) {
		var requestedURL string
		png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9WlD3p8AAAAASUVORK5CYII=")
		importer := Importer{
			Resolver: resolverFunc(publicResolver),
			newClient: func(_ time.Duration, _ Resolver) *http.Client {
				return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					requestedURL = request.URL.String()
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(png))), ContentLength: int64(len(png)), Request: request}, nil
				})}
			},
		}
		_, err := importer.Fetch(context.Background(), "http://images.example/photo.png")
		if err != nil {
			t.Fatalf("expected http→https upgrade to succeed, got error: %v", err)
		}
		if !strings.HasPrefix(requestedURL, "https://") {
			t.Fatalf("expected upgraded https:// request, got %q", requestedURL)
		}
	})

	t.Run("non-http non-https scheme is rejected", func(t *testing.T) {
		_, err := (Importer{Resolver: resolverFunc(publicResolver)}).Fetch(context.Background(), "ftp://images.example/photo.png")
		assertFailureCode(t, err, "HTTPS_REQUIRED")
	})

	t.Run("size limit", func(t *testing.T) {
		importer := Importer{
			MaxBytes: 3,
			Resolver: resolverFunc(publicResolver),
			newClient: func(_ time.Duration, _ Resolver) *http.Client {
				return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("1234")), ContentLength: 4, Request: request}, nil
				})}
			},
		}
		_, err := importer.Fetch(context.Background(), "https://images.example/photo.png")
		assertFailureCode(t, err, "IMAGE_TOO_LARGE")
	})

	t.Run("not an image", func(t *testing.T) {
		importer := Importer{
			Resolver: resolverFunc(publicResolver),
			newClient: func(_ time.Duration, _ Resolver) *http.Client {
				return &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not an image")), ContentLength: 12, Request: request}, nil
				})}
			},
		}
		_, err := importer.Fetch(context.Background(), "https://images.example/photo.txt")
		assertFailureCode(t, err, "UNSUPPORTED_IMAGE")
	})
}

func TestResolvePublicHostRejectsPrivateAndMixedDNSAnswers(t *testing.T) {
	_, err := resolvePublicHost(context.Background(), resolverFunc(func(_ context.Context, _ string, _ string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	}), "internal.example")
	assertFailureCode(t, err, "UNSAFE_SOURCE")

	_, err = resolvePublicHost(context.Background(), resolverFunc(func(_ context.Context, _ string, _ string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}), "mixed.example")
	assertFailureCode(t, err, "UNSAFE_SOURCE")
}

func assertFailureCode(t *testing.T, err error, want string) {
	t.Helper()
	failure, ok := err.(*Failure)
	if !ok || failure.Code != want {
		t.Fatalf("error = %#v, want failure code %q", err, want)
	}
}

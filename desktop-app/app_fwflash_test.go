package main

import (
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"streborn-app/bosefw"
)

func fwTestFile(t *testing.T, payload []byte) (bosefw.File, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	oldPrefix, oldDir := fwDownloadPrefix, fwCacheDir
	dir := t.TempDir()
	fwDownloadPrefix = srv.URL + "/"
	fwCacheDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { fwDownloadPrefix, fwCacheDir = oldPrefix, oldDir })
	name := "Update_ti_27.0.6.46330.5043500.scm.stu"
	return bosefw.File{Name: name, URL: srv.URL + "/" + name, Length: int64(len(payload)), CRC: crc32.ChecksumIEEE(payload)}, srv
}

func TestFetchBoseFileChecksAndCaches(t *testing.T) {
	payload := bytes.Repeat([]byte("bose"), 50000)
	f, srv := fwTestFile(t, payload)
	a := &App{}
	path, err := a.fetchBoseFile(context.Background(), "h", f)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, payload) {
		t.Fatal("cached file differs from the download")
	}
	// A second call must use the cached copy, not download again.
	srv.Close()
	if _, err := a.fetchBoseFile(context.Background(), "h", f); err != nil {
		t.Fatalf("second fetch should use the cache: %v", err)
	}
}

func TestFetchBoseFileRejectsWrongCRC(t *testing.T) {
	f, _ := fwTestFile(t, []byte("not the real image"))
	f.CRC ^= 1
	_, err := (&App{}).fetchBoseFile(context.Background(), "h", f)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("want a checksum refusal, got %v", err)
	}
	dir, _ := fwCacheDir()
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Fatalf("a bad download must not stay in the cache: %v", left)
	}
}

func TestFetchBoseFileWithReportsProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("bose"), 50000)
	f, _ := fwTestFile(t, payload)
	var last int64
	if _, err := (&App{}).fetchBoseFileWith(context.Background(), "", f, func(n int64) { last = n }); err != nil {
		t.Fatal(err)
	}
	if last != int64(len(payload)) {
		t.Errorf("progress ended at %d of %d", last, len(payload))
	}
	dir, _ := fwCacheDir()
	_ = os.RemoveAll(dir)
	f.CRC ^= 1
	if _, err := (&App{}).fetchBoseFileWith(context.Background(), "", f, nil); !errors.Is(err, errBoseFileMismatch) {
		t.Errorf("want errBoseFileMismatch, got %v", err)
	}
}

func TestFetchBoseFileRefusesForeignHost(t *testing.T) {
	_, err := (&App{}).fetchBoseFile(context.Background(), "h", bosefw.File{Name: "x.stu", URL: "https://example.com/x.stu", Length: 1, CRC: 1})
	if err == nil {
		t.Fatal("a link outside Bose's host must be refused")
	}
}

// uploadRecord is what fakeUpdatePage saw. Guarded: the handler runs on the
// server goroutine, and when it hangs up without a reply nothing else orders
// its writes before the test's reads.
type uploadRecord struct {
	mu   sync.Mutex
	got  []byte
	name string
}

func (u *uploadRecord) body() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.got
}

func (u *uploadRecord) fileName() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.name
}

// fakeUpdatePage reads the upload like Bose's page does and records the file.
func fakeUpdatePage(t *testing.T, reply func(w http.ResponseWriter)) (*httptest.Server, *uploadRecord) {
	t.Helper()
	rec := &uploadRecord{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		p, err := mr.NextPart()
		if err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		if p.FormName() != "filename" {
			t.Errorf("field %q, want filename", p.FormName())
		}
		b, _ := io.ReadAll(p)
		rec.mu.Lock()
		rec.name, rec.got = p.FileName(), b
		rec.mu.Unlock()
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "img.stu")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUploadBoseFileSendsTheFormField(t *testing.T) {
	payload := bytes.Repeat([]byte{1, 2, 3}, 100000)
	srv, rec := fakeUpdatePage(t, func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) })
	err := (&App{}).uploadBoseFile(context.Background(), "h", srv.URL+"/update.html", writeTemp(t, payload), "Update_x.stu")
	if err != nil {
		t.Fatal(err)
	}
	if got, name := rec.body(), rec.fileName(); name != "Update_x.stu" || !bytes.Equal(got, payload) {
		t.Fatalf("page got %q with %d bytes", name, len(got))
	}
}

func TestUploadBoseFileClosedWithoutReplyIsSent(t *testing.T) {
	srv, rec := fakeUpdatePage(t, func(w http.ResponseWriter) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("no hijacker")
		}
		c, _, _ := hj.Hijack()
		_ = c.Close()
	})
	payload := bytes.Repeat([]byte{9}, 200000)
	if err := (&App{}).uploadBoseFile(context.Background(), "h", srv.URL, writeTemp(t, payload), "Update_x.stu"); err != nil {
		t.Fatalf("all bytes in and the page hung up: that is not a failure, got %v", err)
	}
	if got := rec.body(); len(got) != len(payload) {
		t.Fatalf("page read %d of %d bytes", len(got), len(payload))
	}
}

func TestUploadBoseFileRefusal(t *testing.T) {
	srv, _ := fakeUpdatePage(t, func(w http.ResponseWriter) { http.Error(w, "wrong file", http.StatusBadRequest) })
	err := (&App{}).uploadBoseFile(context.Background(), "h", srv.URL, writeTemp(t, []byte("abc")), "Update_x.stu")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestUploadReplyLost(t *testing.T) {
	if !uploadReplyLost(io.EOF) || !uploadReplyLost(errors.New("read tcp: connection reset by peer")) {
		t.Fatal("EOF and reset after the body are a lost reply")
	}
	if uploadReplyLost(errors.New("dial tcp: connection refused")) {
		t.Fatal("a refused connection is a real failure")
	}
}

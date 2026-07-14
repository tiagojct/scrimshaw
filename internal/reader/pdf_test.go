package reader

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tiagojct/scrimshaw/internal/fetch"
	"github.com/tiagojct/scrimshaw/internal/store"
)

func TestIsPDF(t *testing.T) {
	if !isPDF("application/pdf", nil) {
		t.Fatal("Content-Type: application/pdf should be detected")
	}
	if !isPDF("application/pdf; charset=binary", nil) {
		t.Fatal("a Content-Type with parameters should still match")
	}
	if !isPDF("", []byte("%PDF-1.7\n...")) {
		t.Fatal("the %PDF- file signature should be detected when Content-Type is unhelpful")
	}
	if isPDF("text/html", []byte("<html></html>")) {
		t.Fatal("an HTML page should not be detected as a PDF")
	}
}

func TestPdfHTML(t *testing.T) {
	out := pdfHTML("para one\n\npara <two> & more")
	if !strings.Contains(out, "<p>para one</p>") {
		t.Fatalf("first paragraph missing: %q", out)
	}
	if !strings.Contains(out, "&lt;two&gt;") || !strings.Contains(out, "&amp;") {
		t.Fatalf("special characters should be escaped, not live markup: %q", out)
	}
	if strings.Contains(out, "<two>") {
		t.Fatalf("PDF text must never produce live HTML tags: %q", out)
	}
}

func TestFallbackTitleFromText(t *testing.T) {
	if got := fallbackTitleFromText("one two three", 12); got != "one two three" {
		t.Fatalf("short text should be returned as-is: %q", got)
	}
	words := strings.Repeat("word ", 20)
	got := fallbackTitleFromText(strings.TrimSpace(words), 12)
	if strings.Count(got, "word") != 12 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long text should be truncated to 12 words with an ellipsis: %q", got)
	}
}

func TestExtractPDFFromRealFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	title, author, siteName, content, text, err := extractPDF(body)
	if err != nil {
		t.Fatal(err)
	}
	if author != "" || siteName != "" {
		t.Fatalf("a PDF has no author/siteName concept, want both empty: author=%q siteName=%q", author, siteName)
	}
	if !strings.Contains(text, "This is a heading") {
		t.Fatalf("extracted text missing known fixture content: %q", text)
	}
	if !strings.HasPrefix(title, "This is a heading") {
		t.Fatalf("title should derive from the extracted text, got %q", title)
	}
	if !strings.Contains(content, "<p>") || strings.Contains(content, "<script") {
		t.Fatalf("content should be safe sanitized HTML: %q", content)
	}
}

func TestExtractPDFRejectsAnEmptyOrCorruptPDF(t *testing.T) {
	if _, _, _, _, _, err := extractPDF([]byte("not a pdf")); err == nil {
		t.Fatal("garbage bytes should fail to open as a PDF")
	}
}

// pdfTransport serves a fixed Content-Type and body for any request, so
// Save()'s full pipeline can be exercised against a real PDF fixture without
// a real listener.
type pdfTransport struct {
	contentType string
	body        []byte
}

func (t pdfTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {t.contentType}},
		Body:       io.NopCloser(strings.NewReader(string(t.body))),
		Request:    req,
	}, nil
}

func TestSaveExtractsRealTextFromAFetchedPDF(t *testing.T) {
	body, err := os.ReadFile("testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), t.TempDir()+"/items.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	saver := &Saver{
		Store:     db,
		Client:    &fetch.Client{HTTP: &http.Client{Timeout: time.Second, Transport: pdfTransport{contentType: "application/pdf", body: body}}},
		Snapshots: t.TempDir(),
	}
	id, err := saver.Save(context.Background(), "https://example.test/paper.pdf", nil)
	if err != nil {
		t.Fatal(err)
	}
	item, err := db.Item(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	// A PDF that fails to fetch/parse would have fallen back to a bare link
	// (see the earlier NYT-style fallback) — confirm this is the real thing,
	// not that degraded path.
	if !item.SnapshotPath.Valid {
		t.Fatal("a successfully extracted PDF should still get a snapshot")
	}
	if !strings.Contains(item.ExtractedText, "This is a heading") {
		t.Fatalf("item should carry the PDF's real extracted text, got %q", item.ExtractedText)
	}
	if item.Title == "" || strings.Contains(item.Title, "example.test") {
		t.Fatalf("title should derive from the PDF's own text, not fall back to the host: %q", item.Title)
	}
}

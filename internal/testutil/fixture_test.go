package testutil_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/go-serper/internal/testutil"
)

func TestFixture(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("testdata", "dir"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join("testdata", "dir", "f.json"), []byte(`{}`), 0o600))

	assert.Equal(t, []byte(`{}`), testutil.Fixture(t, "dir/f.json"), "a slash-separated path under testdata")

	rec := &recorder{}
	assert.Nil(t, testutil.Fixture(rec, "dir/missing.json"))
	assert.True(t, rec.failed, "a missing fixture fails the test")
}

func TestSearchBody(t *testing.T) {
	for _, n := range []int{0, 1, 10, 100} {
		var v struct {
			SearchParameters map[string]any   `json:"searchParameters"`
			Organic          []map[string]any `json:"organic"`
			Credits          int              `json:"credits"`
		}
		require.NoError(t, json.Unmarshal(testutil.SearchBody(n), &v), "n=%d", n)
		assert.Len(t, v.Organic, n)
		assert.NotNil(t, v.SearchParameters)
		assert.Equal(t, 1, v.Credits)
		for i, o := range v.Organic {
			assert.InDelta(t, float64(i+1), o["position"], 0, "positions are 1-based and in order")
		}
	}
}

func TestImagesBody(t *testing.T) {
	for _, n := range []int{0, 1, 100} {
		var v struct {
			SearchParameters map[string]any   `json:"searchParameters"`
			Images           []map[string]any `json:"images"`
		}
		require.NoError(t, json.Unmarshal(testutil.ImagesBody(n), &v), "n=%d", n)
		assert.Len(t, v.Images, n)
		assert.NotNil(t, v.SearchParameters)
	}
}

func TestScrapeBody(t *testing.T) {
	for _, md := range []bool{false, true} {
		var v struct {
			Text     string  `json:"text"`
			Markdown *string `json:"markdown"`
		}
		require.NoError(t, json.Unmarshal(testutil.ScrapeBody(1000, md), &v))
		assert.Len(t, v.Text, 1000)
		assert.Equal(t, md, v.Markdown != nil)
	}
}

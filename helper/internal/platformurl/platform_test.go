package platformurl

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	t.Run("accepts and canonicalizes supported single-video URLs", func(t *testing.T) {
		tests := []struct {
			name      string
			raw       string
			provider  Provider
			canonical string
		}{
			{
				name:      "YouTube watch",
				raw:       "https://www.youtube.com/watch?v=_mVb1D8wHxg",
				provider:  YouTube,
				canonical: "https://www.youtube.com/watch?v=_mVb1D8wHxg",
			},
			{
				name:      "YouTube watch without www and tracking parameters",
				raw:       "https://youtube.com/watch?v=_mVb1D8wHxg&list=PLignored&utm_source=test",
				provider:  YouTube,
				canonical: "https://www.youtube.com/watch?v=_mVb1D8wHxg",
			},
			{
				name:      "YouTube watch with encoded tracking parameter",
				raw:       "https://www.youtube.com/watch?v=_mVb1D8wHxg&utm_source=foo%20bar",
				provider:  YouTube,
				canonical: "https://www.youtube.com/watch?v=_mVb1D8wHxg",
			},
			{
				name:      "YouTube Shorts",
				raw:       "https://youtube.com/shorts/abc_123-XYZ",
				provider:  YouTube,
				canonical: "https://www.youtube.com/shorts/abc_123-XYZ",
			},
			{
				name:      "YouTube short link",
				raw:       "https://youtu.be/abc_123-XYZ?t=4",
				provider:  YouTube,
				canonical: "https://youtu.be/abc_123-XYZ",
			},
			{
				name:      "YouTube short link with encoded tracking parameter",
				raw:       "https://youtu.be/abc_123-XYZ?si=foo%20bar",
				provider:  YouTube,
				canonical: "https://youtu.be/abc_123-XYZ",
			},
			{
				name:      "Bilibili BV",
				raw:       "https://www.bilibili.com/video/BV1K3Gz6pEoo/?spm_id_from=x",
				provider:  Bilibili,
				canonical: "https://www.bilibili.com/video/BV1K3Gz6pEoo",
			},
			{
				name:      "Bilibili av part",
				raw:       "https://www.bilibili.com/video/av170001?p=2",
				provider:  Bilibili,
				canonical: "https://www.bilibili.com/video/av170001?p=2",
			},
			{
				name:      "Bilibili av part with encoded tracking parameter",
				raw:       "https://www.bilibili.com/video/av170001?p=2&spm_id_from=foo%2Fbar",
				provider:  Bilibili,
				canonical: "https://www.bilibili.com/video/av170001?p=2",
			},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				got, err := Classify(test.raw)
				if err != nil {
					t.Fatalf("Classify(%q) returned error: %v", test.raw, err)
				}
				want := Video{Provider: test.provider, CanonicalURL: test.canonical}
				if got != want {
					t.Fatalf("Classify(%q) = %#v, want %#v", test.raw, got, want)
				}
			})
		}
	})

	t.Run("rejects URLs outside the trust boundary", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
		}{
			{name: "empty", raw: ""},
			{name: "malformed", raw: "://www.youtube.com/watch?v=_mVb1D8wHxg"},
			{name: "HTTP", raw: "http://www.youtube.com/watch?v=_mVb1D8wHxg"},
			{name: "credentials", raw: "https://user:secret@www.youtube.com/watch?v=_mVb1D8wHxg"},
			{name: "explicit port", raw: "https://www.youtube.com:443/watch?v=_mVb1D8wHxg"},
			{name: "empty port syntax", raw: "https://www.youtube.com:/watch?v=_mVb1D8wHxg"},
			{name: "Unicode lookalike host", raw: "https://www.yоutube.com/watch?v=_mVb1D8wHxg"},
			{name: "suffix-confusion host", raw: "https://www.youtube.com.evil.example/watch?v=_mVb1D8wHxg"},
			{name: "YouTube empty video ID", raw: "https://www.youtube.com/watch?v="},
			{name: "YouTube short video ID", raw: "https://www.youtube.com/watch?v=abc"},
			{name: "YouTube non-ASCII video ID", raw: "https://www.youtube.com/watch?v=abc_123-中文"},
			{name: "YouTube punctuation in video ID", raw: "https://www.youtube.com/watch?v=abc.123-XYZ"},
			{name: "YouTube duplicate video ID", raw: "https://www.youtube.com/watch?v=_mVb1D8wHxg&v=abc_123-XYZ"},
			{name: "YouTube encoded video ID", raw: "https://www.youtube.com/watch?v=%5FmVb1D8wHxg"},
			{name: "YouTube encoded video key", raw: "https://www.youtube.com/watch?%76=_mVb1D8wHxg"},
			{name: "YouTube empty Shorts ID", raw: "https://www.youtube.com/shorts/"},
			{name: "YouTube extra Shorts path", raw: "https://www.youtube.com/shorts/abc_123-XYZ/more"},
			{name: "YouTube short link short ID", raw: "https://youtu.be/abc"},
			{name: "YouTube short link non-ASCII ID", raw: "https://youtu.be/abc_123-中文"},
			{name: "YouTube short link extra path", raw: "https://youtu.be/abc_123-XYZ/more"},
			{name: "YouTube playlist", raw: "https://www.youtube.com/playlist?list=PL123"},
			{name: "YouTube channel", raw: "https://www.youtube.com/channel/UC123"},
			{name: "YouTube search", raw: "https://www.youtube.com/results?search_query=test"},
			{name: "YouTube live", raw: "https://www.youtube.com/live/abc_123-XYZ"},
			{name: "YouTube fragment fake ID", raw: "https://www.youtube.com/watch#v=_mVb1D8wHxg"},
			{name: "YouTube valid ID with fragment", raw: "https://www.youtube.com/watch?v=_mVb1D8wHxg#v=abc_123-XYZ"},
			{name: "Bilibili empty video ID", raw: "https://www.bilibili.com/video/"},
			{name: "Bilibili malformed BV ID", raw: "https://www.bilibili.com/video/BV1K3Gz6pEo"},
			{name: "Bilibili malformed av ID", raw: "https://www.bilibili.com/video/avabc"},
			{name: "Bilibili zero av ID", raw: "https://www.bilibili.com/video/av0"},
			{name: "Bilibili encoded video ID", raw: "https://www.bilibili.com/video/%42V1K3Gz6pEoo"},
			{name: "Bilibili extra video path", raw: "https://www.bilibili.com/video/BV1K3Gz6pEoo/more"},
			{name: "Bilibili invalid part", raw: "https://www.bilibili.com/video/av170001?p=two"},
			{name: "Bilibili encoded part", raw: "https://www.bilibili.com/video/av170001?p=%32"},
			{name: "Bilibili encoded part key", raw: "https://www.bilibili.com/video/av170001?%70=2"},
			{name: "Bilibili duplicate part", raw: "https://www.bilibili.com/video/av170001?p=1&p=2"},
			{name: "Bilibili list", raw: "https://www.bilibili.com/medialist/play/123"},
			{name: "Bilibili bangumi", raw: "https://www.bilibili.com/bangumi/play/ep123"},
			{name: "Bilibili live", raw: "https://live.bilibili.com/123"},
			{name: "Bilibili fragment fake ID", raw: "https://www.bilibili.com/video/#BV1K3Gz6pEoo"},
			{name: "excessive URL", raw: "https://www.youtube.com/watch?v=_mVb1D8wHxg&x=" + strings.Repeat("a", 2048)},
		}

		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				if got, err := Classify(test.raw); err == nil {
					t.Fatalf("Classify(%q) = %#v, want error", test.raw, got)
				}
			})
		}
	})
}

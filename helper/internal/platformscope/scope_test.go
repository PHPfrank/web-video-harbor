package platformscope

import "testing"

func TestExperimentalPageClassifiesOnlyTrustedHTTPSHosts(t *testing.T) {
	for _, rawURL := range []string{
		"https://youtube.com/watch?v=x",
		"https://www.youtube.com/watch?v=x",
		"https://m.youtube.com/shorts/x",
		"https://youtu.be/x",
		"https://bilibili.com/video/x",
		"https://www.bilibili.com/video/x",
		"https://channels.weixin.qq.com/",
		"https://weixin.qq.com/",
		"https://www.wechat.com/",
		"https://www.youtube.com/中文路径?v=x",
		"https://WWW.YOUTUBE.COM/watch?v=x",
	} {
		if !IsExperimentalPage(rawURL) {
			t.Errorf("IsExperimentalPage(%q) = false, want true", rawURL)
		}
	}
}

func TestExperimentalPageRejectsConfusionAndUntrustedURLs(t *testing.T) {
	for _, rawURL := range []string{
		"http://youtube.com/watch?v=x",
		"https://user@youtube.com/watch?v=x",
		"https://youtube.com:443/watch?v=x",
		"https://youtube.com.example/watch?v=x",
		"https://notyoutube.com/watch?v=x",
		"https://sub.youtu.be/x",
		"https://例子.youtube.com/watch?v=x",
		"https://yоutube.com/watch?v=x",
		"https://xn--youtube-9jg.com/watch?v=x",
		"https://example.com/watch?next=https://youtube.com/x",
		"https://example.com/#https://youtube.com/x",
		"https://cdn.example/youtube.com/video.mp4",
		"//youtube.com/watch?v=x",
		"not a url",
		"",
	} {
		if IsExperimentalPage(rawURL) {
			t.Errorf("IsExperimentalPage(%q) = true, want false", rawURL)
		}
	}
}

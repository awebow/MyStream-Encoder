package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/disintegration/imaging"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/tidwall/gjson"
)

type VideoInfo struct {
	Width    int
	Height   int
	Duration float64
	FPS      float64
	Bitrate  int
	Codec    string
}

func GetVideoInfo(src string) (*VideoInfo, error) {
	info := &VideoInfo{}

	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		src,
	)
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	info.Duration, _ = strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)

	cmd = exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_streams", src)
	raw, err = cmd.Output()
	if err != nil {
		return nil, err
	}

	data := gjson.ParseBytes(raw)

	for _, stream := range data.Get("streams").Array() {
		if stream.Get("codec_type").String() == "video" {
			info.Width = int(stream.Get("width").Int())
			info.Height = int(stream.Get("height").Int())

			fpsInfo := strings.Split(stream.Get("avg_frame_rate").String(), "/")
			numer, _ := strconv.ParseFloat(fpsInfo[0], 64)
			denom, _ := strconv.ParseFloat(fpsInfo[1], 64)
			info.FPS = numer / denom
			info.Bitrate = int(stream.Get("bit_rate").Int())
			info.Codec = stream.Get("codec_name").String()
		}
	}

	return info, nil
}

func (app *App) ProcessVideo(uploadID, videoID string, info *VideoInfo) ([]*VideoInfo, error) {
	qualities := []EncodeOption{}

	maxBitrate := 0
	for _, q := range app.Config.Qualities {
		if info.FPS < q.FpsFilter {
			continue
		}

		if info.Width*9 >= info.Height*16 && info.Width >= q.Width {
			// 16:9 이상의 화면비
			qualities = append(qualities, EncodeOption{
				q.Name,
				q.Width, -1,
				q.FrameRate, q.Bitrate,
				q.PriorSrc,
			})
		} else if info.Width*9 < info.Height*16 && info.Height >= q.Height {
			// 16:9 미만의 화면비
			qualities = append(qualities, EncodeOption{
				q.Name,
				-1, q.Height,
				q.FrameRate, q.Bitrate,
				q.PriorSrc,
			})
		} else {
			continue
		}

		if q.Bitrate > maxBitrate {
			maxBitrate = q.Bitrate
		}
	}

	bitrateScale := float64(info.Bitrate) / float64(maxBitrate) / 1000
	if bitrateScale > 1 {
		bitrateScale = 1
	}

	os.Mkdir("videos/"+videoID, 0644)
	os.Mkdir("videos/"+videoID+"/encode", 0644)
	os.Mkdir("videos/"+videoID+"/dash", 0644)

	var encoder VideoEncoder
	if app.Config.HWAccel == "cuda" {
		encoder = app.NewCudaEncoder()
	} else {
		encoder = app.NewCPUEncoder()
	}

	result := make([]*VideoInfo, len(qualities))

	tracks := []string{}
	for i, q := range qualities {
		src := "videos/" + uploadID
		dst := "videos/" + videoID + "/encode/" + q.Name + ".mp4"
		tracks = append(tracks, dst)

		fmt.Println("Encoding " + dst + "...")

		for _, p := range q.PriorSrc {
			s := "videos/" + videoID + "/encode/" + p + ".mp4"
			if _, err := os.Stat(s); err == nil {
				src = s
				break
			}
		}

		q.Bitrate = int(float64(q.Bitrate) * bitrateScale)

		err := encoder.EncodeVideo(
			src, dst,
			q.Width, q.Height,
			"h264", q.FrameRate, q.Bitrate,
		)

		if err != nil {
			return result, err
		}

		result[i], err = GetVideoInfo(dst)
		if err != nil {
			return result, err
		}
	}

	audio := "videos/" + videoID + "/encode/audio.mp4"
	err := EncodeAudio("videos/"+uploadID, audio, "aac", 2, 128)
	if err != nil {
		return result, err
	}

	err = GenerateMPD("videos/"+videoID+"/dash/video.mpd", audio, tracks...)
	if err != nil {
		return result, err
	}

	sort.Slice(result, func(a, b int) bool {
		return result[a].Bitrate > result[b].Bitrate
	})

	return result, app.GenerateThumbnail(
		"videos/"+uploadID,
		"videos/"+videoID+"/dash/thumbnail.jpg",
		float32(info.Duration/2),
	)
}

type VideoEncoder interface {
	EncodeVideo(
		src string, dst string,
		width int, height int,
		codec string, frameRate float64, bitrate int) error
}

type CPUEncoder struct {
	gopSize int
	preset  string
}

func (app *App) NewCPUEncoder() *CPUEncoder {
	return &CPUEncoder{
		gopSize: app.Config.GOPSize,
		preset:  app.Config.Preset,
	}
}

func (encoder *CPUEncoder) EncodeVideo(
	src, dst string,
	width, height int,
	codec string, frameRate float64, bitrate int) error {

	cmd := exec.Command(
		"ffmpeg", "-y",
		"-i", src,
		"-vf", fmt.Sprintf("scale=%d:%d, fps=%.2f", width, height, frameRate),
		"-c:v", codec,
		"-b:v", fmt.Sprintf("%dK", bitrate),
		"-g", fmt.Sprint(encoder.gopSize),
		"-keyint_min", fmt.Sprint(encoder.gopSize),
		"-preset", encoder.preset,
		"-an",
		dst,
	)

	return cmd.Run()
}

type CudaEncoder struct {
	gopSize int
	preset  string
}

func (app *App) NewCudaEncoder() *CudaEncoder {
	return &CudaEncoder{
		gopSize: app.Config.GOPSize,
		preset:  app.Config.Preset,
	}
}

func (encoder *CudaEncoder) EncodeVideo(
	src, dst string,
	width, height int,
	codec string, frameRate float64, bitrate int) error {

	if codec == "h264" {
		codec = "h264_nvenc"
	}

	cmd := exec.Command(
		"ffmpeg", "-y",
		"-hwaccel", "cuda",
		"-hwaccel_output_format", "cuda",
		"-i", src,
		"-vf", fmt.Sprintf("scale_cuda=%d:%d, fps=%.2f", width, height, frameRate),
		"-c:v", codec,
		"-b:v", fmt.Sprintf("%dK", bitrate),
		"-g", fmt.Sprint(encoder.gopSize),
		"-keyint_min", fmt.Sprint(encoder.gopSize),
		"-preset", encoder.preset,
		"-an",
		dst,
	)
	return cmd.Run()
}

func EncodeAudio(src, dst, codec string, channel, bitrate int) error {
	if codec == "opus" {
		codec = "libopus"
	}

	cmd := exec.Command(
		"ffmpeg", "-y",
		"-i", src,
		"-vn",
		"-c:a", codec,
		"-ac", fmt.Sprint(channel),
		"-b:a", fmt.Sprintf("%dK", bitrate),
		dst,
	)
	return cmd.Run()
}

type EncodeOption struct {
	Name      string
	Width     int
	Height    int
	FrameRate float64
	Bitrate   int
	PriorSrc  []string
}

func GenerateMPD(dst string, audioSrc string, videoSrcs ...string) error {
	dir := dst[:strings.LastIndex(dst, "/")]

	sp := strings.Split(audioSrc, "/")
	args := []string{"in=" + audioSrc + ",stream=audio,output=" + dir + "/" + sp[len(sp)-1]}
	for _, s := range videoSrcs {
		sp = strings.Split(s, "/")
		args = append(args, "in="+s+",stream=video,output="+dir+"/"+sp[len(sp)-1])
	}
	args = append(args, "--mpd_output", dst)

	cmd := exec.Command(
		"packager",
		args...,
	)
	buf := new(bytes.Buffer)
	cmd.Stderr = buf
	err := cmd.Run()
	return err
}

func (app *App) ParseAuth(auth string) (string, bool) {
	s := strings.Split(auth, " ")
	if len(s) == 2 && s[0] == "Bearer" {
		token, err := jwt.Parse([]byte(s[1]), jwt.WithVerify(jwa.HS256, []byte(app.Config.UploadSignKey)))
		if err == nil {
			id, ok := token.Get("video_id")
			if ok {
				return id.(string), true
			}
		}
	}

	return "", false
}

func (app *App) GenerateThumbnail(src string, dst string, position float32) error {
	err := exec.Command(
		"ffmpeg",
		"-ss", fmt.Sprint(position),
		"-i", src,
		"-vframes", "1",
		dst,
	).Run()
	if err != nil {
		return err
	}

	img, err := imaging.Open(dst)
	if err != nil {
		return err
	}

	result := imaging.Fill(img, app.Config.Thumbnail.Width, app.Config.Thumbnail.Height,
		imaging.Center, imaging.Lanczos)

	return imaging.Save(result, dst, imaging.JPEGQuality(app.Config.Thumbnail.Quality))
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-resty/resty/v2"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/tus/tusd/pkg/filestore"
	tusd "github.com/tus/tusd/pkg/handler"
)

func main() {
	os.Mkdir("videos", 0644)
	app := NewApp()

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodDelete,
			http.MethodPatch,
			http.MethodOptions,
			http.MethodHead,
		},
		AllowHeaders: []string{"*"},
	}))

	handler := echo.WrapHandler(http.StripPrefix("/videos/", app.uploadHandler))
	e.Any("/videos/*path", func(c echo.Context) error {
		if _, ok := app.ParseAuth(c.Request().Header.Get("Authorization")); ok {
			return handler(c)
		} else {
			return c.NoContent(http.StatusUnauthorized)
		}
	})

	go app.ListenEvents()
	fmt.Println("MyStream Encoder started on " + app.Config.Listen)
	e.Logger.Fatal(e.Start(app.Config.Listen))
}

type App struct {
	Config struct {
		Listen    string `json:"listen"`
		APIURL    string `json:"api_url"`
		HWAccel   string `json:"hwaccel"`
		Preset    string `json:"preset"`
		GOPSize   int    `json:"gop_size"`
		Qualities []struct {
			Name      string   `json:"name"`
			Width     int      `json:"width"`
			Height    int      `json:"height"`
			FrameRate float64  `json:"frame_rate"`
			Bitrate   int      `json:"bitrate"`
			FpsFilter float64  `json:"fps_filter"`
			PriorSrc  []string `json:"prior_src"`
		} `json:"qualities"`
		Thumbnail struct {
			Width   int `json:"width"`
			Height  int `json:"height"`
			Quality int `json:"quality"`
		} `json:"thumbnail"`
		UploadSignKey string `json:"upload_sign_key"`
		Store         struct {
			Type        string   `json:"type"`
			AWSEndpoint string   `json:"aws_endpoint,omitempty"`
			Bucket      string   `json:"bucket,omitempty"`
			Command     []string `json:"command,omitempty"`
		} `json:"store"`
	}

	uploadHandler *tusd.Handler
}

func NewApp() *App {
	app := new(App)
	app.Config.Preset = "medium"
	app.Config.GOPSize = 60

	data, _ := ioutil.ReadFile("config.json")
	json.Unmarshal(data, &app.Config)

	store := filestore.FileStore{
		Path: "./videos",
	}

	composer := tusd.NewStoreComposer()
	store.UseIn(composer)

	app.uploadHandler, _ = tusd.NewHandler(tusd.Config{
		BasePath:                "/videos/",
		StoreComposer:           composer,
		NotifyCompleteUploads:   true,
		RespectForwardedHeaders: true,
	})

	return app
}

func (app *App) ListenEvents() {
	defer func() {
		if r := recover(); r != nil {
			app.ListenEvents()
		}
	}()

	for {
		event := <-app.uploadHandler.CompleteUploads

		videoID, _ := app.ParseAuth(event.HTTPRequest.Header.Get("Authorization"))
		app.HandleUpload(event.Upload.ID, videoID)
	}
}

func (app *App) HandleUpload(uploadID, videoID string) {
	info, err := GetVideoInfo("videos/" + uploadID)

	var result []*VideoInfo
	if err == nil {
		result, err = app.ProcessVideo(uploadID, videoID, info)
	} else {
		fmt.Println(err)
	}

	if err == nil {
		err = app.StoreFile("videos/"+videoID+"/dash", videoID)
	} else {
		fmt.Println(err)
	}

	client := resty.New()
	token := jwt.New()
	token.Set(jwt.IssuerKey, "encoder")
	token.Set("video_id", videoID)
	bearer, _ := jwt.Sign(token, jwa.HS256, []byte(app.Config.UploadSignKey))

	if err == nil {
		client.R().
			SetHeader("Authorization", "Bearer "+string(bearer)).
			SetBody(map[string]interface{}{
				"width":      result[0].Width,
				"height":     result[0].Height,
				"frame_rate": int(result[0].FPS),
				"duration":   info.Duration,
				"status":     "ACTIVE",
				"posted_at":  time.Now(),
			}).
			Put(app.Config.APIURL + "/videos/" + videoID)

	} else {
		client.R().
			SetHeader("Authorization", "Bearer "+string(bearer)).
			Delete(app.Config.APIURL + "/videos/" + videoID)
	}

	os.RemoveAll("videos/" + videoID)
	os.Remove("videos/" + uploadID)
	os.Remove("videos/" + uploadID + ".info")
}

func (app *App) StoreFile(src string, dst string) error {
	if strings.EqualFold(app.Config.Store.Type, "s3") {
		cfg, err := config.LoadDefaultConfig(context.TODO(),
			config.WithSharedCredentialsFiles([]string{"aws/credentials"}),
			config.WithSharedConfigFiles([]string{"aws/config"}),
			config.WithEndpointResolver(aws.EndpointResolverFunc(func(service, region string) (aws.Endpoint, error) {
				if app.Config.Store.AWSEndpoint != "" {
					return aws.Endpoint{
						URL:           app.Config.Store.AWSEndpoint,
						SigningRegion: region,
					}, nil
				}

				return aws.Endpoint{}, &aws.EndpointNotFoundError{}
			})))
		if err != nil {
			return err
		}

		client := s3.NewFromConfig(cfg)
		return app.storeS3(client, src, dst)
	}

	replacer := strings.NewReplacer("${src}", src, "${dst}", dst)
	args := make([]string, len(app.Config.Store.Command)-1)
	for i := range args {
		args[i] = replacer.Replace(app.Config.Store.Command[i+1])
	}

	return exec.Command(app.Config.Store.Command[0], args...).Run()
}

func (app *App) storeS3(client *s3.Client, src string, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		files, err := ioutil.ReadDir(src)
		if err != nil {
			return err
		}

		for _, fi := range files {
			err = app.storeS3(client, src+"/"+fi.Name(), dst+"/"+fi.Name())
			if err != nil {
				return err
			}
		}

		return nil
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
			Bucket: aws.String(app.Config.Store.Bucket),
			Key:    aws.String(dst),
			Body:   f,
			ACL:    types.ObjectCannedACLPublicRead,
		})

		return err
	}
}

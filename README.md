# MyStream Encoder
MyStream Encoder는 사용자가 업로드한 동영상을 웹 브라우저에서 재생 가능한 형식으로 품질별로 나누어 인코딩하고 mpd 파일을 생성하여 MPEG-DASH로 재생 가능하게 합니다.
동영상 파일은 [tus](https://tus.io) 프로토콜을 이용하여 업로드하고, 업로드 된 영상은 FFmpeg를 통해 인코딩하고 Shaka Packager를 통해 DASH 영상으로 변환합니다.

## Getting Started
### 요구사항
MyStream Encoder는 다음 항목들이 설치되어 있고 PATH에 등록되어 있어야 실행 가능합니다.

* [FFmpeg](https://www.ffmpeg.org)
* [Shaka Packager](https://github.com/google/shaka-packager)
* [Go](https://www.ffmpeg.org)(소스코드에서 빌드/실행 시)

### 설치
GitHub [Release 페이지](https://github.com/awebow/MyStream-Encoder/releases)에서 최신 릴리즈의 바이너리를 다운로드 받거나 다음 명령어를 통해 소스코드를 clone 합니다.
```console
$ git clone https://github.com/awebow/MyStream-Encoder
```

### 설정
바이너리 혹은 소스코드가 저장된 디렉토리로 이동하여 `config.json` 파일을 작성합니다.

```console
$ cd MyStream-Encoder
```

`config.json` 파일 구성의 예는 다음과 같습니다.
```json
{
    "listen": ":8080",
    "api_url": "https://api.mystream.mshnet.xyz",
    "hwaccel": "cuda",
    "qualities": [
        {
            "name": "UHD",
            "width": 3840,
            "height": 2160,
            "frame_rate": 30,
            "bitrate": 40000,
            "fps_filter": 0,
            "prior_src": []
        },
        {
            "name": "FHD",
            "width": 1920,
            "height": 1080,
            "frame_rate": 30,
            "bitrate": 8000,
            "fps_filter": 0,
            "prior_src": ["UHD"]
        },
        {
            "name": "HD",
            "width": 1280,
            "height": 720,
            "frame_rate": 30,
            "bitrate": 5000,
            "fps_filter": 0,
            "prior_src": ["FHD", "UHD"]
        }
    ],
    "thumbnail": {
        "width": 854,
        "height": 480,
        "quality": 70
    },
    "upload_sign_key": "da!cjxZX!&*dc31",
    "store": {
        "type": "s3",
        "bucket": "mystream.videos",
        "aws_endpoint": "https://cnf6czg8zeh6.compat.objectstorage.ap-seoul-1.oraclecloud.com"
    }
}
```

설정 파일의 각 필드에 대한 설명은 다음과 같습니다.

* `listen` - 서버의 listen address. 필수
* `api_url` - API 서버의 URL. 필수
* `hwaccel` - 사용할 하드웨어 가속.
* `qualities` - 인코딩 품질 리스트. 1개 이상 필수
    * `name` - 품질 이름. 필수
    * `width` - 가로 해상도(px). 필수
    * `height` - 세로 해상도(px). 필수
    * `frame_rate` - 프레임 레이트. 필수
    * `bitrate` - 인코딩 비트레이트. 필수
    * `fps_filter` - 소스 영상 프레임 레이트 하한선. 소스 영상의 프레임 레이트가 이 값 이상일 경우에만 이 품질을 인코딩합니다.
    * `prior_src` - 입력 선호 품질 목록. 인코딩 시 이 목록의 품질 영상을 입력 영상으로 선호합니다. 낮은 품질의 영상을 입력 영상으로 사용하여 디코딩 속도를 향상시킬 수 있지만 출력 영상의 품질이 저하될 수 있습니다.
* `thumbnail` - 동영상 썸네일 이미지 설정. 필수
    * `width` - 가로 크기(px)
    * `height` - 세로 크기(px)
    * `quality` - JPEG 압축 퀄리티(1~100)
* `upload_sign_key` - 동영상 업로드에서 인증에 사용할 JWT sign key. Mystream API 설정의 `upload_sign_key`와 동일해야합니다.
* `storage` - 동영상 저장소 설정. 필수
    * `type` - 저장소 유형. `s3` 또는 `custom`
    * `bucket` - S3 버킷 이름. 저장소 유형이 `s3`일 경우 필수
    * `aws_endpoint` - 사용자 지정 AWS 엔드포인트.
    * `command` - 저장 명령어 지정. `${src}`는 파일의 상대 경로, `${dst}`는 저장할 상대 경로. 저장소 유형이 `custom`일 경우 필수

#### AWS S3 설정
동영상 저장소로 AWS S3를 사용하는 경우, aws 디렉토리의 `config`, `credential` 파일에 AWS 설정을 작성해야합니다. 다음 예를 참고하세요.

**config**
```ini
[default]
region = ap-northeast-2
```

```ini
[default]
aws_access_key_id = [계정 Access Key]
aws_secret_access_key = [계정 Secret Key]
```

### 실행
바이너리를 사용하는 경우는 다음 명령어를 통해 서버를 실행합니다.
```console
$ ./mystream-encoder
```

소스 코드를 사용하는 경우에는 다음 명령어를 통해 서버를 실행합니다.
```console
$ go run .
```

### 소스 코드 빌드
소스 코드를 빌드하여 바이너리를 생성하기 위해서는 다음 명령어를 실행합니다.
```console
$ go build
```

## Troubleshooting
### Reverse Proxy 사용 시 업로드가 되지 않는 경우
`Nginx`나 `Apache` 등의 웹 서버 프로그램을 통해 MyStream Encoder에 Reverse Proxy를 적용하는 경우 `X-Forwarded-Host`와 `X-Forwarded-Proto`를 원본 요청과 동일하게 전달해줘야합니다.

다음은 `Nginx`를 사용하는 경우의 설정의 예입니다.
```nginx
server {
    listen 443 ssl;
    server_name encoder.mystream.mshnet.xyz;
    ssl_certificate_key C:\Certbot\live\encoder.mystream.mshnet.xyz\privkey.pem;
    ssl_certificate C:\Certbot\live\encoder.mystream.mshnet.xyz\cert.pem;

    location / {
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_pass http://127.0.0.1:8000;
    }
}
```

또한 업로드 영상의 크기가 허용량을 초과하여 업로드가 차단될 수 있으니 최대 Body 사이즈도 적정 수준으로 늘려주는 것이 좋습니다.
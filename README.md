# discordyt
Self-hosted Discord bot that supports playback of YouTube videos in voice channels.

## Setup
1. [Install go](https://go.dev/doc/install)
2. Install ffmpeg
	1. sudo apt install ffmpeg
3. Install [yt-dlp](https://github.com/yt-dlp/yt-dlp)
	1. Download yt-dlp executable version
	3. Move to /usr/local/bin
	4. Make executable (chmod +x)
4. Clone repo
5. Copy .env-sample
6. Set bot properties
7. Set youtube api key
8. Set path to store store downloaded songs and log files
9. "go run cmd/discordyt/* ENV_FILE"
10. Or "go install cmd/discordyt/*"
	1. Executable will be installed in $GOPATH, which is $HOME/go by default, so recommend adding ~/go/bin to PATH
11. Optional
	1. Setup a cron job to keep the app alive
	2. Setup a cron job to delete files in song folder

(I still need to elaborate on some of the steps here)

module github.com/metux/starfleetctl

go 1.22

require github.com/X11Libre/go-x11proto v0.0.0

require (
	github.com/robfig/cron/v3 v3.0.1 // indirect
	golang.org/x/image v0.18.0 // indirect
	golang.org/x/sys v0.8.0 // indirect
	golang.org/x/text v0.16.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/X11Libre/go-x11proto => /home/nekrad/src/xorg/mpbt-workspace/_WORK_/go-x11proto/sources/xlibre/go-x11proto

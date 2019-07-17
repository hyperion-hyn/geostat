# STEP 1 build executable binary from alpine:golang
FROM golang:alpine as builder

ENV APP_RUN_DIR=/go/bin/geostat/
# Install git
RUN apk add --update --no-cache git build-base

# copy code
COPY ./ $GOPATH/src/github.com/hyperion-hyn/geostat

#build the dmapper server and crontab
WORKDIR $GOPATH/src/github.com/hyperion-hyn/geostat
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo -ldflags="-w -s" -o $APP_RUN_DIR/geostat \
        && rm -rf $GOPATH/src/github.com/hyperion-hyn/

# step 2 build a small image from alpine
FROM alpine

ENV APP_ETC_DIR=/etc/geostat/
ENV APP_STORE_DIR=/go/bin/geostat/

# run mkdir
RUN mkdir -p $APP_ETC_DIR

# install supervisor
RUN apk add --update --no-cache ca-certificates
# copy file from builder
COPY --from=builder $APP_STORE_DIR/geostat /usr/bin/geostat

#copy config file
COPY ./geostat.json $APP_ETC_DIR/geostat.json
COPY ./GeoIP2-City.mmdb $APP_ETC_DIR/GeoLite2-City.mmdb

WORKDIR $APP_ETC_DIR
ENTRYPOINT ["/usr/bin/geostat", "--logfile", "/var/log/geostat/access.log", "--geodb", "/etc/geostat/GeoLite2-City.mmdb"]
# Build from the repository root, because the example uses the framework
# through replace directives:
#
#	docker build -f examples/http-users/Dockerfile -t http-users .
#
# or use compose.yaml in this folder, which also starts Jaeger for traces.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY observe/go.mod observe/go.sum ./observe/
COPY examples/http-users/go.mod examples/http-users/go.sum ./examples/http-users/
RUN cd examples/http-users && go mod download
COPY . .
RUN cd examples/http-users && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/http-users .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/http-users /http-users
ENV APP_ENV=production PORT=9900
EXPOSE 9900
USER nonroot:nonroot
ENTRYPOINT ["/http-users"]

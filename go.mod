module github.com/whale-net/everything

go 1.25.0

require (
	github.com/coreos/go-oidc/v3 v3.17.0
	github.com/docker/docker v28.5.2+incompatible
	github.com/docker/go-connections v0.6.0
	github.com/docker/go-units v0.5.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.3 // indirect
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/gorilla/securecookie v1.1.2 // indirect
	github.com/gorilla/sessions v1.4.0
	github.com/jackc/pgx/v5 v5.8.0
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rabbitmq/amqp091-go v1.10.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/oauth2 v0.35.0
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9 // indirect
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11 // indirect
)

require (
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.16.0
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.40.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.40.0
	go.opentelemetry.io/otel/log v0.16.0
	go.opentelemetry.io/otel/sdk v1.43.0
	go.opentelemetry.io/otel/sdk/log v0.16.0
	go.opentelemetry.io/otel/sdk/metric v1.43.0
)

require go.temporal.io/sdk v1.42.0

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.2 // indirect
	github.com/moby/sys/atomicwriter v0.1.0 // indirect
	github.com/nexus-rpc/sdk-go v0.6.0 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.43.0 // indirect
	go.temporal.io/api v1.62.7 // indirect
	golang.org/x/time v0.15.0 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)

require (
	github.com/a-h/templ v0.3.1001
	github.com/aws/aws-sdk-go-v2/feature/s3/manager v1.22.2
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/containerd/errdefs/pkg v0.3.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.28.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.61.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.10.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260401024825-9d38bb4040a9 // indirect
)

require (
	github.com/aws/aws-sdk-go-v2 v1.41.1
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.4 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.9
	github.com/aws/aws-sdk-go-v2/credentials v1.19.9 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.8 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.96.0
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.14 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.6 // indirect
	github.com/aws/smithy-go v1.24.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/errdefs v1.0.0
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/distribution/reference v0.6.0
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.64.0
	go.opentelemetry.io/otel v1.43.0
	go.opentelemetry.io/otel/metric v1.43.0
	go.opentelemetry.io/otel/trace v1.43.0
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

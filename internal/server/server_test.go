package server

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	api "github.com/wreckitral/proglog/api/v1"
	"github.com/wreckitral/proglog/internal/log"
	"github.com/wreckitral/proglog/internal/config"
    "google.golang.org/grpc/credentials"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

func TestServer(t *testing.T) {
	for scenario, fn := range map[string]func(
		t *testing.T,
		client api.LogClient,
		config *Config,
	){
		"produce/consume a message to/from the log succeeds":
            testProduceConsume,
		"produce/consume stream succeeds":
            testProduceConsumeStream,
		"consume past log boundary fails":
            testConsumePastBoundary,
	} {
		t.Run(scenario, func(t *testing.T) {
			client, config, teardown := setupTest(t, nil)
			defer teardown()
			fn(t, client, config)
		})
	}
}

func setupTest(t *testing.T, fn func(*Config)) (
    client api.LogClient,
    cfg *Config,
    teardown func(),
) {
    t.Helper()

    l, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)

    clientTLSConfig, err := config.SetupTLSConfig(config.TLSConfig{
        CAFile: config.CAFile,
    })
    require.NoError(t, err)

    clientCreds := credentials.NewTLS(clientTLSConfig)

    cc, err := grpc.NewClient(
        l.Addr().String(),
        grpc.WithTransportCredentials(clientCreds),
    )
    require.NoError(t, err)

    client = api.NewLogClient(cc)

    serverTLSConfig, err := config.SetupTLSConfig(config.TLSConfig{
        CertFile: config.ServerCertFile,
        KeyFile: config.ServerKeyFile,
        CAFile: config.CAFile,
        ServerAddress: l.Addr().String(),
    })
    require.NoError(t, err)
    serverCreds := credentials.NewTLS(serverTLSConfig)

    dir, err := os.MkdirTemp("", "server-test")
    require.NoError(t, err)

    clog, err := log.NewLog(dir, log.Config{})
    require.NoError(t, err)

    cfg = &Config{
        CommitLog: clog,
    }
    if fn != nil {
        fn(cfg)
    }
    server, err := NewGRPCServer(cfg, grpc.Creds(serverCreds))
    require.NoError(t, err)

    go func() {
        server.Serve(l)
    }()

    return client, cfg, func() {
        server.Stop()
        cc.Close()
        l.Close()
    }
}

func testProduceConsume(t *testing.T, client api.LogClient, config *Config) {
    ctx := context.Background()

    want := &api.Record{
        Value: []byte("hello world"),
    }

    produce, err := client.Produce(
        ctx,
        &api.ProduceRequest{
            Record: want,
        },
    )

    require.NoError(t, err)

    consume, err := client.Consume(ctx, &api.ConsumeRequest{
        Offset: produce.Offset,
    })

    require.NoError(t, err)
    require.Equal(t, want.Value, consume.Record.Value)
    require.Equal(t, want.Offset, consume.Record.Offset)
}

// tests that server responds with an api.ErrOffsetOutOfRange when a
// client tries to consume beyond the log's boundaries
func testConsumePastBoundary(
	t *testing.T,
	client api.LogClient,
	config *Config,
) {
	ctx := context.Background()

	produce, err := client.Produce(ctx, &api.ProduceRequest{
		Record: &api.Record{
			Value: []byte("hello world"),
		},
	})
	require.NoError(t, err)

	consume, err := client.Consume(ctx, &api.ConsumeRequest{
		Offset: produce.Offset + 1,
	})
	if consume != nil {
		t.Fatal("consume not nill")
	}

	got := status.Code(err)
	want := status.Code(api.ErrOffsetOutOfRange{}.GRPCStatus().Err())
	if got != want {
		t.Fatalf("got err: %v, want: %v", got, want)
	}
}

func testProduceConsumeStream(
	t *testing.T,
	client api.LogClient,
	config *Config,
) {
	ctx := context.Background()
	records := []*api.Record{
		{
			Value:  []byte("first message"),
			Offset: 0,
		},
		{
			Value:  []byte("second message"),
			Offset: 1,
		},
	}
	{
		stream, err := client.ProduceStream(ctx)
		require.NoError(t, err)

		for offset, record := range records {
			err = stream.Send(&api.ProduceRequest{
				Record: record,
			})
			require.NoError(t, err)

			res, err := stream.Recv()
			require.NoError(t, err)
			if res.Offset != uint64(offset) {
				t.Fatalf(
					"got offset: %d, want: %d",
					res.Offset,
					record.Offset,
				)
			}
		}
	}
	{
		stream, err := client.ConsumeStream(
            ctx,
            &api.ConsumeRequest{Offset: 0},
        )
		require.NoError(t, err)

		for i, record := range records {
			res, err := stream.Recv()
			require.NoError(t, err)
			require.Equal(t, res.Record, &api.Record{
				Value:  record.Value,
				Offset: uint64(i),
			})
		}
	}
}


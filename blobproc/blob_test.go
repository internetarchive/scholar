package blobproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestBlobPath(t *testing.T) {
	var cases = []struct {
		about   string
		folder  string
		sha1hex string
		ext     string
		prefix  string
		result  string
	}{
		{
			about:   "empty",
			folder:  "",
			sha1hex: "4e1243bd22c66e76c2ba9eddc1f91394e57f9f83",
			ext:     "",
			prefix:  "",
			result:  "/4e/12/4e1243bd22c66e76c2ba9eddc1f91394e57f9f83",
		},
		{
			about:   "folder",
			folder:  "images",
			sha1hex: "4e1243bd22c66e76c2ba9eddc1f91394e57f9f83",
			ext:     "",
			prefix:  "",
			result:  "images/4e/12/4e1243bd22c66e76c2ba9eddc1f91394e57f9f83",
		},
		{
			about:   "folder, ext",
			folder:  "images",
			sha1hex: "4e1243bd22c66e76c2ba9eddc1f91394e57f9f83",
			ext:     "xml",
			prefix:  "",
			result:  "images/4e/12/4e1243bd22c66e76c2ba9eddc1f91394e57f9f83.xml",
		},
		{
			about:   "folder, ext, prefix",
			folder:  "images",
			sha1hex: "4e1243bd22c66e76c2ba9eddc1f91394e57f9f83",
			ext:     "xml",
			prefix:  "dev-",
			result:  "dev-images/4e/12/4e1243bd22c66e76c2ba9eddc1f91394e57f9f83.xml",
		},
	}
	for _, c := range cases {
		result := blobPath(c.folder, c.sha1hex, c.ext, c.prefix)
		if result != c.result {
			t.Fatalf("[%s] got %v, want %v", c.about, result, c.result)
		}
	}
}

func TestPutGetObject(t *testing.T) {
	var hostPort string
	switch os.Getenv("TEST_LOCAL_MINIO") {
	case "":
		skipNoDocker(t)
		if testing.Short() {
			t.Skip("skipping testcontainer based tests in short mode")
		}
		ctx := context.Background()
		req := testcontainers.ContainerRequest{
			Image: "quay.io/minio/minio:latest",
			ExposedPorts: []string{
				"9000/tcp",
				"9001/tcp",
			},
			WaitingFor: wait.ForListeningPort("9000/tcp"),
			Cmd: []string{
				"minio",
				"server",
				"/tmp",
			},
		}
		minioC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			t.Fatalf("could not start minio: %s", err)
		}
		defer func() {
			if err := minioC.Terminate(ctx); err != nil {
				t.Fatalf("could not stop minio: %s", err)
			}
		}()
		ip, err := minioC.Host(ctx)
		if err != nil {
			t.Fatalf("testcontainer: count not get host: %v", err)
		}
		port, err := minioC.MappedPort(ctx, "9000")
		if err != nil {
			t.Fatalf("testcontainer: count not get port: %v", err)
		}
		hostPort = fmt.Sprintf("%s:%s", ip, port.Port())
		t.Logf("starting e2e test, using minio container %s running at %v", minioC.GetContainerID(), hostPort)
	default:
		hostPort = fmt.Sprintf("0.0.0.0:9000")
		t.Logf("starting e2e test, using local minio running at %v", hostPort)
	}
	blobStore, err := NewBlobStore(hostPort, &BlobStoreOptions{
		AccessKey:     "minioadmin",
		SecretKey:     "minioadmin",
		DefaultBucket: "default",
		UseSSL:        false,
	})
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	var cases = []struct {
		opts         *BlobRequestOptions
		expectedPath string
	}{
		{
			opts: &BlobRequestOptions{
				Folder:  "f",
				SHA1Hex: "", // should be calculated if not given
				Blob:    []byte("hello, world!"),
				Prefix:  "",
				Ext:     "",
			},
			expectedPath: "f/1f/09/1f09d30c707d53f3d16c530dd73d70a6ce7596a9",
		},
		{
			opts: &BlobRequestOptions{
				Folder:  "",
				SHA1Hex: "", // should be calculated if not given
				Blob:    []byte("123"),
				Prefix:  "",
				Ext:     "",
			},
			expectedPath: "/40/bd/40bd001563085fc35165329ea1ff5c5ecbdbbeef",
		},
		{
			opts: &BlobRequestOptions{
				Folder:  "",
				SHA1Hex: "", // should be calculated if not given
				Blob:    []byte("123"),
				Prefix:  "",
				Ext:     "tei.xml",
			},
			// TODO: minio will strip any leading slash?
			expectedPath: "/40/bd/40bd001563085fc35165329ea1ff5c5ecbdbbeef.tei.xml",
		},
		{
			opts: &BlobRequestOptions{
				Folder:  "thumbnails",
				SHA1Hex: "", // should be calculated if not given
				Blob:    []byte("123"),
				Prefix:  "dev-",
				Ext:     "png",
			},
			expectedPath: "dev-thumbnails/40/bd/40bd001563085fc35165329ea1ff5c5ecbdbbeef.png",
		},
	}
	for _, c := range cases {
		resp, err := blobStore.PutBlob(context.TODO(), c.opts)
		if err != nil {
			t.Fatalf("PutBlob failed: %v", err)
		}
		if want := c.expectedPath; resp.ObjectPath != want {
			t.Fatalf("[put] got %v, want %v", resp.ObjectPath, want)
		} else {
			t.Logf("successfully saved blob: %v", resp.ObjectPath)
		}
		// c.opts will have the SHA1 field amended, hacky, because invisible
		b, err := blobStore.GetBlob(context.TODO(), c.opts)
		if err != nil {
			t.Fatalf("GetBlob failed: %v", err)
		}
		if want := string(c.opts.Blob); string(b) != want {
			t.Fatalf("[get] got %v, want %v", string(b), want)
		}
		t.Logf("successfully retrieved blob: %v", resp.ObjectPath)
	}
}

func skipNoDocker(t *testing.T) {
	noDocker := false
	cmd := exec.Command("systemctl", "is-active", "docker")
	b, err := cmd.CombinedOutput()
	if err != nil {
		noDocker = true
	}
	if strings.TrimSpace(string(b)) != "active" {
		noDocker = true
	}
	if !noDocker {
		// We found some docker.
		return
	}
	// Otherwise, try podman.
	_, err = exec.LookPath("podman")
	if err == nil {
		t.Logf("podman detected")
		// DOCKER_HOST=unix:///run/user/$UID/podman/podman.sock
		usr, err := user.Current()
		if err != nil {
			t.Logf("cannot get UID, set DOCKER_HOST manually")
		} else {
			sckt := fmt.Sprintf("unix:///run/user/%v/podman/podman.sock", usr.Uid)
			os.Setenv("DOCKER_HOST", sckt)
			t.Logf("set DOCKER_HOST to %v", sckt)
		}
		noDocker = false
	}
	if noDocker {
		t.Skipf("docker not installed or not running")
	}
}

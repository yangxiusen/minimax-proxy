package remote

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/inputspool"
	"minimax-h3-tc/internal/netguard"
)

var pngInput = []byte("\x89PNG\r\n\x1a\ninput-bytes")

func imageRequest(raw string) string {
	b, _ := json.Marshal(domain.GenerationRequest{Model: "seed-model", Duration: 5, Content: []domain.GenerationContent{{Type: "text", Text: "test"}, {Type: "image_url", ImageURL: &domain.MediaURL{URL: raw}, Role: "reference_image"}}})
	return string(b)
}

func TestInputLocalSpoolValidatesBytesAndSHA256(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			p, s, db, task, f := processorFixture(t)
			root := t.TempDir()
			p.Inputs.Root = root
			prepared, err := inputspool.New(root).PrepareRequest(context.Background(), task.TaskID, []byte(imageRequest("data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngInput))))
			if err != nil {
				t.Fatal(err)
			}
			file := prepared.Files[0]
			_, err = db.Exec(`INSERT INTO task_input_spool_files(id,task_id,content_index,content_type,role,source_kind,declared_mime,detected_mime,media_type,extension,relative_path,size_bytes,sha256,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,1,1)`, file.ID, file.TaskID, file.ContentIndex, file.ContentType, file.Role, file.SourceKind, file.DeclaredMIME, file.DetectedMIME, file.MediaType, file.Extension, file.RelativePath, file.SizeBytes, file.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			task.RequestJSON = string(prepared.JSON)
			if corrupt {
				data := append([]byte(nil), pngInput...)
				data[len(data)-1] ^= 1
				if err = os.WriteFile(filepath.Join(root, filepath.FromSlash(file.RelativePath)), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = p.ProcessTask(context.Background(), task)
			got, _ := s.Get(context.Background(), "owner", task.TaskID)
			if corrupt {
				if err != nil || got.Status != domain.StatusFailed || len(f.keys) != 0 || f.uploads != 0 {
					t.Fatal("corrupt file submitted", err, got.Status)
				}
			} else {
				if !errors.Is(err, domain.ErrRemotePending) || len(f.keys) != 1 || f.uploads != 1 {
					t.Fatal(err, len(f.keys), f.uploads)
				}
				asset, err := s.GetRemoteAsset(context.Background(), task.TaskID, "node", 1)
				if err != nil || asset.SourceSHA256 != file.SHA256 || asset.SizeBytes != int64(len(pngInput)) {
					t.Fatal(asset, err)
				}
				if !strings.Contains(f.bodies[0], "asset://"+assetID) || strings.Contains(f.bodies[0], "proxy-input") {
					t.Fatal(f.bodies[0])
				}
			}
		})
	}
}

type publicResolver struct{}

func (publicResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
}
func sourceGuard(server *httptest.Server) *netguard.Guard {
	return netguard.New(netguard.Options{Resolver: publicResolver{}, Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}})
}

type inputRecords struct {
	AssetStore
	files []domain.InputSpoolFile
}

func (s inputRecords) ListInputSpoolFiles(context.Context, string) ([]domain.InputSpoolFile, error) {
	return s.files, nil
}

func TestInputURLAndOSSStreamingReuseWithoutLocalFiles(t *testing.T) {
	for _, oss := range []bool{false, true} {
		t.Run(fmt.Sprint(oss), func(t *testing.T) {
			p, s, _, task, f := processorFixture(t)
			downloads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				downloads++
				if r.Header.Get("Authorization") != "" {
					t.Error("node token sent to source")
				}
				w.Header().Set("Content-Type", "image/png")
				w.Write(pngInput)
			}))
			defer srv.Close()
			root := t.TempDir()
			p.Inputs.Root = root
			p.Inputs.Guard = sourceGuard(srv)
			task.RequestJSON = imageRequest("http://media.example/input")
			if oss {
				hash := sha256.Sum256(pngInput)
				p.Inputs.Store = inputRecords{AssetStore: s, files: []domain.InputSpoolFile{{ID: "input-oss", TaskID: task.TaskID, ContentIndex: 1, ContentType: "image_url", Role: "reference_image", SourceKind: "data_uri", MediaType: "image/png", Extension: ".png", RelativePath: "missing/not-local.png", ObjectURL: "http://media.example/input", SizeBytes: int64(len(pngInput)), SHA256: hex.EncodeToString(hash[:])}}}
			}
			run, err := s.AcquireRemoteLease(context.Background(), task.TaskID, "node", "inputs-worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			first, err := p.Inputs.Prepare(context.Background(), task, run, f)
			if err != nil {
				t.Fatal(err)
			}
			second, err := p.Inputs.Prepare(context.Background(), task, run, f)
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != string(second) || downloads != 1 || f.uploads != 1 {
				t.Fatal("asset not reused", downloads, f.uploads)
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 0 {
				t.Fatal("OSS input created local files")
			}
		})
	}
}

func TestInputUnsafeSourcesAndUnknownReferencesNeverSubmit(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/private", "http://169.254.169.254/latest/meta-data", "file:///private", "asset://" + assetID, "proxy-input://other/input"} {
		t.Run(raw, func(t *testing.T) {
			p, _, _, task, f := processorFixture(t)
			task.RequestJSON = imageRequest(raw)
			p.ProcessTask(context.Background(), task)
			if len(f.keys) != 0 || f.uploads != 0 {
				t.Fatal("unsafe input sent")
			}
		})
	}
}

func TestInputRedirectToPrivateHostIsBlocked(t *testing.T) {
	p, _, _, task, f := processorFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
	}))
	defer srv.Close()
	p.Inputs.Guard = sourceGuard(srv)
	task.RequestJSON = imageRequest("http://media.example/input")
	p.ProcessTask(context.Background(), task)
	if len(f.keys) != 0 || f.uploads != 0 {
		t.Fatal("unsafe redirect sent")
	}
}

func TestInputUploadFailureCreatesNoGeneration(t *testing.T) {
	p, _, _, task, f := processorFixture(t)
	f.uploadErr = errors.New("lost upload response")
	task.RequestJSON = imageRequest("data:image/png;base64," + base64.StdEncoding.EncodeToString(pngInput))
	if err := p.ProcessTask(context.Background(), task); !errors.Is(err, domain.ErrRemotePending) {
		t.Fatal(err)
	}
	if len(f.keys) != 0 {
		t.Fatal("created generation despite upload failure")
	}
}

func TestInputTotalAudioVideoDurationAndCounts(t *testing.T) {
	p, s, _, task, f := processorFixture(t)
	f.assetDuration = 6
	request := domain.GenerationRequest{Model: "seed-model", Duration: 5, Content: []domain.GenerationContent{{Type: "text", Text: "test"}}}
	for i := 0; i < 3; i++ {
		request.Content = append(request.Content, domain.GenerationContent{Type: "audio_url", Role: "reference_audio", AudioURL: &domain.MediaURL{URL: "data:audio/mpeg;base64," + base64.StdEncoding.EncodeToString([]byte("ID3audio"))}})
	}
	data, _ := json.Marshal(request)
	task.RequestJSON = string(data)
	if err := p.ProcessTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(context.Background(), "owner", task.TaskID)
	if got.Status != domain.StatusFailed || len(f.keys) != 0 || f.uploads != 3 {
		t.Fatal("duration >15 seconds allowed", got.Status, f.uploads)
	}
	request.Content = append(request.Content, request.Content[1])
	data, _ = json.Marshal(request)
	task.RequestJSON = string(data)
	_, err := p.Inputs.Prepare(context.Background(), task, domain.RemoteRun{}, f)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatal("audio count >3 allowed", err)
	}
}

func TestInputStreamRejectsLimitWithoutHashingOverflow(t *testing.T) {
	stream := &inputStream{reader: strings.NewReader("123456"), hash: sha256.New(), limit: 5}
	var data [20]byte
	if _, err := stream.Read(data[:]); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
}

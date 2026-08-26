package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"shieldtunnel/catalog"
	"shieldtunnel/domain"
	"shieldtunnel/ring"
	"shieldtunnel/store"
)

func TestMain(m *testing.M) {
	if os.Getenv("SHIELD_LIFECYCLE_TEST_CHILD") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestModel_GracefulShutdownDrainsAcceptedRingLock(t *testing.T) {
	cases := []struct {
		name       string
		signal     os.Signal
		grace      time.Duration
		finishBody bool
	}{
		{name: "SIGINT drains an accepted upload", signal: os.Interrupt, grace: 5 * time.Second, finishBody: true},
		{name: "SIGTERM drains an accepted upload", signal: syscall.SIGTERM, grace: 5 * time.Second, finishBody: true},
		{name: "deadline cancels an unfinished upload atomically", signal: syscall.SIGTERM, grace: 200 * time.Millisecond, finishBody: false},
	}

	for caseIndex, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("reserve address: %v", err)
			}
			addr := probe.Addr().String()
			if err := probe.Close(); err != nil {
				t.Fatalf("release address: %v", err)
			}

			dbPath := filepath.Join(t.TempDir(), "lifecycle.db")
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(),
				"SHIELD_LIFECYCLE_TEST_CHILD=1",
				"SHIELD_ADDR="+addr,
				"SHIELD_DB="+dbPath,
				"SHIELD_SHUTDOWN_TIMEOUT="+tc.grace.String(),
			)
			var childLog bytes.Buffer
			cmd.Stdout = &childLog
			cmd.Stderr = &childLog
			if err := cmd.Start(); err != nil {
				t.Fatalf("start server child: %v", err)
			}
			childDone := make(chan error, 1)
			go func() { childDone <- cmd.Wait() }()
			t.Cleanup(func() {
				if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
					_ = cmd.Process.Kill()
					<-childDone
				}
			})

			deadline := time.Now().Add(5 * time.Second)
			for {
				conn, dialErr := net.DialTimeout("tcp", addr, 50*time.Millisecond)
				if dialErr == nil {
					_ = conn.Close()
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("server did not listen: %v\n%s", dialErr, childLog.String())
				}
				time.Sleep(10 * time.Millisecond)
			}

			summary, err := catalog.NewStatic().Summarize("澄江路—望塔站", "通用楔形环")
			if err != nil {
				t.Fatalf("catalog summary: %v", err)
			}
			geometry := domain.GrooveGeometry{WidthMM: 12, DepthMM: 8, CornerMM: 4, JointPosMM: 20}
			holes := domain.HoleGeometry{Count: 12, SpacingMM: 60}
			segments := make([]domain.Segment, 0, 6)
			for i, typ := range []domain.SegmentType{domain.SegmentKey, domain.SegmentAdj, domain.SegmentAdj, domain.SegmentStd, domain.SegmentStd, domain.SegmentStd} {
				segments = append(segments, domain.Segment{Seq: i, Type: typ, CenterAngle: []int64{30, 60, 60, 70, 70, 70}[i], Wedge: domain.WedgeLeft, Groove: geometry, Holes: holes})
			}
			joints := make([]domain.Joint, 0, 12)
			for i := 0; i < 6; i++ {
				joints = append(joints,
					domain.Joint{Type: domain.JointLongitudinal, EdgeA: domain.SegmentEdge{SegmentSeq: i, Side: "right"}, EdgeB: domain.SegmentEdge{SegmentSeq: (i + 1) % 6, Side: "left"}},
					domain.Joint{Type: domain.JointCircum, EdgeA: domain.SegmentEdge{SegmentSeq: i, Side: "front"}, EdgeB: domain.SegmentEdge{SegmentSeq: i, Side: "back"}},
				)
			}
			ringNo := domain.RingNo(caseIndex + 100)
			opID := fmt.Sprintf("shutdown-lock-%d", caseIndex)
			body, err := json.Marshal(ring.LockRequest{
				OperationID: opID, Section: "澄江路—望塔站", RingNo: ringNo,
				RingType: "通用楔形环", Generation: 1, RuleSummary: summary,
				Segments: segments, Joints: joints,
			})
			if err != nil {
				t.Fatalf("marshal lock request: %v", err)
			}

			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				t.Fatalf("connect upload: %v", err)
			}
			defer conn.Close()
			headers := fmt.Sprintf("POST /api/rings HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", addr, len(body))
			if _, err := conn.Write(append([]byte(headers), body[:len(body)-1]...)); err != nil {
				t.Fatalf("write partial request: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			if err := cmd.Process.Signal(tc.signal); err != nil {
				t.Fatalf("signal server: %v", err)
			}

			closeDeadline := time.Now().Add(time.Second)
			for {
				newConn, dialErr := net.DialTimeout("tcp", addr, 30*time.Millisecond)
				if dialErr != nil {
					break
				}
				_ = newConn.Close()
				if time.Now().After(closeDeadline) {
					t.Fatal("server continued accepting new connections after shutdown signal")
				}
				time.Sleep(10 * time.Millisecond)
			}

			if tc.finishBody {
				if _, err := conn.Write(body[len(body)-1:]); err != nil {
					t.Fatalf("finish accepted upload: %v\n%s", err, childLog.String())
				}
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatalf("read complete HTTP response: %v\n%s", err, childLog.String())
				}
				defer resp.Body.Close()
				var response struct {
					Code string          `json:"code"`
					Task json.RawMessage `json:"task"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.StatusCode != http.StatusOK || response.Code != "ok" || len(response.Task) == 0 {
					t.Fatalf("response status=%d code=%q task=%s", resp.StatusCode, response.Code, response.Task)
				}
			}

			select {
			case err := <-childDone:
				if err != nil {
					t.Fatalf("server exit: %v\n%s", err, childLog.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("server did not exit after shutdown\n%s", childLog.String())
			}

			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("reopen database: %v", err)
			}
			defer db.Close()
			seq, err := db.LastSeq(context.Background())
			if err != nil {
				t.Fatalf("read event sequence: %v", err)
			}
			if tc.finishBody {
				if seq != 1 {
					t.Fatalf("persisted event count=%d, want 1", seq)
				}
				task, err := ring.NewAggregate(catalog.NewStatic(), db).Graph("澄江路—望塔站", ringNo)
				if err != nil || task.RingNo != ringNo {
					t.Fatalf("persisted ring task=%+v err=%v", task, err)
				}
			} else {
				if seq != 0 {
					t.Fatalf("unfinished request persisted %d events, want 0", seq)
				}
				_, err := ring.NewAggregate(catalog.NewStatic(), db).Graph("澄江路—望塔站", ringNo)
				var domainErr *domain.Error
				if !errors.As(err, &domainErr) || domainErr.Code != domain.CodeNotFound {
					t.Fatalf("unfinished request left a ring task, graph err=%v", err)
				}
			}
		})
	}
}

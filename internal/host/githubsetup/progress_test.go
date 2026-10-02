package githubsetup

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type setupProgressBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *setupProgressBuffer) Write(raw []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(raw)
}

func (b *setupProgressBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestDeviceRequestAnnouncesWorkAndReportsWaitBeforeTransportReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output setupProgressBuffer
		entered, resume := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- RunUser(t.Context(), func(context.Context, UserRequest) (UserView, error) {
				close(entered)
				<-resume
				return UserView{}, UserLoginError("enable Device flow in the GitHub App settings, then retry setup")
			}, Options{NoBrowser: true}, &output)
		}()
		<-entered
		synctest.Wait()
		if !strings.Contains(output.String(), "Requesting a GitHub device code for personal Projects...") {
			t.Fatal("code request remained silent while transport blocked")
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if !strings.Contains(output.String(), "(30s elapsed)") {
			t.Fatal("code request long wait missing", output.String())
		}
		close(resume)
		if err := <-done; err == nil || strings.Contains(output.String(), "GitHub integration ready") {
			t.Fatal("failed device request reported success", err)
		}
		after := output.String()
		time.Sleep(time.Minute)
		synctest.Wait()
		if output.String() != after {
			t.Fatal("heartbeat continued after request failed")
		}
	})
}

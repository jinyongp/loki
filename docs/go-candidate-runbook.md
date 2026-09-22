# Go 후보 설치·검증·복구 Runbook

## 적용 범위

이 문서는 Go 후보 아티팩트를 만들고 격리 환경에서 승인한 뒤 설치·rollback하는 절차다. 후보는 `loki-go-*` unit과 `/etc/loki-go`, `/run/loki-go`, `/var/lib/loki-go`, `/var/cache/loki-go`를 사용한다. Python Loki 상태는 읽기 전용 복사본으로만 가져온다.

## 사전 조건

- Ubuntu 24.04 amd64, systemd, current Docker with Compose v2 and Buildx/BuildKit
- Go 1.27.1
- CLI 프로토콜 3, JSON envelope 1, JSON 출력 방식의 `process start`·`process restart` schema를 제공하는 `devtools` 후보 실행 파일
- `runner`의 전역 Git `user.name`과 `user.email`
- 충돌하지 않는 runner, workspace, browser UID/GID

`devtools` 버전은 `devtools.toml`에서 고정하지 않는다. 빌드 시 전달한 실행 파일과 그 명령 catalog가 후보 아티팩트에 함께 고정되므로 새 버전은 새 후보를 만들어 같은 acceptance를 통과시킨다.

## 후보 생성

```sh
./scripts/build/build-toolchain-bundle.sh /tmp/loki-toolchain-bundle
./scripts/build/build-candidate.sh \
  /tmp/loki-go-candidate \
  "$(command -v devtools)" \
  /tmp/loki-toolchain-bundle
```

빌드는 다운로드 checksum, toolchain manifest 일치, Loki·devtools 버전과 전체 rootfs checksum을 기록한다.

## 격리 승인

```sh
./scripts/verify/accept-candidate.sh /tmp/loki-go-candidate
```

기본 실행은 서로 다른 privileged Ubuntu 컨테이너에서 같은 후보를 두 번 검사한다. 각 실행은 다음을 모두 통과해야 한다.

- Python v1 vault 복사본을 Go vault로 가져오기
- v1 설치 후 v2 원자적 전환
- 모든 systemd 서비스와 네 Unix socket 준비
- secret 상태, devtools, toolchain doctor
- Chromium 실제 시작과 browser RPC
- 격리 signing agent를 사용한 Git commit과 `verify-commit`
- 컨테이너 재부팅 후 자동 기동
- v2에서 v1으로 rollback
- Go migration backup에서 Python vault 복원
- 원본 Python 복사본의 inode와 digest 불변

실패하면 harness는 failed unit과 `loki-go*` journal을 출력한다. 생성한 컨테이너와 acceptance image는 종료 trap이 해당 실행 ID만 정리한다.

## 실제 OCI Job 승인

Docker 가능한 disposable Linux host에서는 별도 fixture 값을 조합하지 않고 다음 한 명령으로 실제 Job lifecycle/network/endpoint 승인을 실행한다.

```sh
./scripts/verify/accept-oci-jobs.sh
```

runner는 현재 Docker Buildx/BuildKit이 준비되어 있는지 먼저 확인한다. 기본 `/var/run/docker.sock`의 peer UID를 확인하고, 임시 shared workspace와 loopback-only Registry 3.1.1을 만든 뒤 현재 checkout에서 gateway 실행 파일과 execution/egress contract만 포함한 최소 fixture 이미지를 BuildKit으로 빌드한다. 이미지를 임시 registry에 push해 immutable `repo@sha256` 참조를 얻은 다음 모든 `TestRealOCIJob*` 케이스를 required mode로 실행하고 자신이 만든 registry/container/workspace/image reference를 정리한다. deprecated legacy Docker builder로의 fallback은 제공하지 않는다. 기본 허용 대상은 committed egress policy에 포함된 `registry.npmjs.org:443`이다.

Docker socket, workspace, allowlisted authority, daemon peer UID 또는 registry helper가 비표준인 호스트에서는 기존 `LOKI_TEST_*`와 `LOKI_OCI_ACCEPTANCE_REGISTRY_IMAGE` 환경변수로 해당 값만 override할 수 있다. 정상적인 local-Docker Linux 경로에서는 수동 image digest나 workspace 준비가 필요하지 않다.

## 릴리스 통합 검증

systemd 후보와 self-hosting 이미지를 함께 승인할 때는 빌드가 끝난 동일 소스 리비전의 산출물 세 개를 전달한다.

```sh
./scripts/verify/verify-release.sh \
  /tmp/loki-go-candidate \
  loki:release-candidate \
  loki-browser:release-candidate
```

이 명령은 전체 Go 테스트, race detector, vet, 격리 systemd 설치·재부팅·rollback, core/browser OCI 메타데이터, WSL2 또는 Linux Compose 수명주기, 파생 이미지, browser profile, credential·network·mount 경계를 순서대로 검사한다. 어느 단계든 실패하면 릴리스 후보는 승인되지 않는다. 현재 배포 서비스로의 전환은 이 검증과 별개다.

## 설치 전 준비

runner Git identity를 확인한다.

```sh
sudo -u runner env HOME=/home/runner git config --global --includes user.name
sudo -u runner env HOME=/home/runner git config --global --includes user.email
```

마이그레이션이 필요하면 Python vault의 `master.key`와 `store.json`을 root 전용 임시 디렉터리에 복사하고 원본 digest를 별도로 기록한다. 설치 명령에는 이 복사본만 전달한다.

## 설치와 상태 확인

```sh
sudo /tmp/loki-go-candidate/install.sh install \
  /tmp/loki-go-candidate RELEASE_ID \
  RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID EXECUTOR_UID \
  registry.example/loki@sha256:... \
  /root/loki-python-vault-copy

sudo /usr/local/sbin/loki-go-lifecycle health
sudo systemctl is-enabled loki-go.target
sudo systemctl --no-pager --full status loki-go.target 'loki-go-*.service'
sudo /usr/local/bin/loki secret status
```

마지막 vault 복사본 인자를 생략하면 기존 Go vault를 그대로 사용한다. Job image 인자는 mutable tag가 아니라 `registry/repository@sha256:...` 형식의 immutable digest reference여야 한다. 설치기는 checksum과 runner/browser/executor identity 충돌을 검사하고, toolchain과 vault migration을 완료한 뒤에만 `current` 링크를 전환한다. health 실패 시 이전 릴리스로 자동 복귀한다.

## Rollback과 vault 복원

```sh
sudo /usr/local/sbin/loki-go-lifecycle rollback
sudo /usr/local/sbin/loki-go-lifecycle health

sudo /usr/local/bin/loki migrate-vault restore \
  --migration /var/lib/loki-go/runtime \
  --destination /root/loki-python-vault-restored
```

복원 결과의 `master.key`와 `store.json` digest를 설치 전 기록과 비교한다. 복원 디렉터리가 이미 있거나 migration fingerprint·backup이 일치하지 않으면 명령은 실패한다.

## 장애 확인

```sh
sudo systemctl --failed --no-pager
sudo journalctl -b --no-pager -u 'loki-go*' -n 300
sudo /usr/local/sbin/loki-go-lifecycle health
```

MCP unit은 runtime, port guard, browser, signing, executor socket을 기다린다. executor는 별도의 privileged launcher에만 연결되고 MCP에는 launcher socket이나 Docker socket이 노출되지 않는다. native launcher unit은 `/run/docker.sock` 경로가 있어야 시작하지만, 빈 Job journal에서의 lifecycle health는 Docker daemon의 실제 OCI 실행 가능성까지 검사하지 않는다. 그 기능은 위 `./scripts/verify/accept-oci-jobs.sh` real OCI/network acceptance가 통과해야 증명된다.

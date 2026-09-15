# Go 후보 설치·검증·복구 Runbook

## 적용 범위

이 문서는 Go 후보 아티팩트를 만들고 격리 환경에서 승인한 뒤 설치·rollback하는 절차다. 후보는 `loki-go-*` unit과 `/etc/loki-go`, `/run/loki-go`, `/var/lib/loki-go`, `/var/cache/loki-go`를 사용한다. Python Loki 상태는 읽기 전용 복사본으로만 가져온다.

## 사전 조건

- Ubuntu 24.04 amd64, systemd, Docker
- Go 1.27.1
- 프로토콜 1과 필요한 `process start`·`process restart` schema를 제공하는 현재 `devtools`
- `runner`의 전역 Git `user.name`과 `user.email`
- 충돌하지 않는 runner, workspace, browser UID/GID

`devtools` 버전은 `devtools.toml`에서 고정하지 않는다. 빌드 시 전달한 실행 파일과 그 명령 catalog가 후보 아티팩트에 함께 고정되므로 새 버전은 새 후보를 만들어 같은 acceptance를 통과시킨다.

## 후보 생성

```sh
./scripts/build-loki-toolchain-bundle.sh /tmp/loki-toolchain-bundle
./scripts/build-loki-go-candidate.sh \
  /tmp/loki-go-candidate \
  "$(command -v devtools)" \
  /tmp/loki-toolchain-bundle
```

빌드는 다운로드 checksum, toolchain manifest 일치, Loki·devtools 버전과 전체 rootfs checksum을 기록한다.

## 격리 승인

```sh
./scripts/accept-loki-go-candidate.sh /tmp/loki-go-candidate
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

## 릴리스 통합 검증

systemd 후보와 self-hosting 이미지를 함께 승인할 때는 빌드가 끝난 동일 소스 리비전의 산출물 세 개를 전달한다.

```sh
./scripts/verify-loki-release.sh \
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
  RUNNER_UID RUNNER_GID WORKSPACE_GID BROWSER_UID \
  /root/loki-python-vault-copy

sudo /usr/local/sbin/loki-go-lifecycle health
sudo systemctl is-enabled loki-go.target
sudo systemctl --no-pager --full status loki-go.target 'loki-go-*.service'
sudo /usr/local/bin/loki secret status
```

마이그레이션 인자를 생략하면 기존 Go vault를 그대로 사용한다. 설치기는 checksum과 identity 충돌을 검사하고, toolchain과 vault migration을 완료한 뒤에만 `current` 링크를 전환한다. health 실패 시 이전 릴리스로 자동 복귀한다.

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

MCP unit은 runtime, port guard, browser, signing socket을 기다린다. runtime은 Docker socket이 늦게 생성되어도 기동하며, 실제 Docker 검사 시점에 socket 오류를 반환한다.

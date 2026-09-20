# Go 전환 실행 계획

> 이 문서는 기존 native/systemd 후보판의 전환 설계와 검증 절차를 보존한 기록이다. 새 일반 배포의 기준은 [설치·배포 계획](installation-distribution-plan.md)과 [아키텍처 개선 계획](architecture-improvement-plan.md)이며, 현재 구현의 결함과 검증 한계는 [Go 종합 검토](go-readiness-review.md)에 기록한다. 아래의 직접 CLI 접근, 고정 toolchain, 상시 필수 구성요소 가정은 새 MCP-only 구조의 요구사항이 아니다. 이전 API·설정과의 호환성은 유지하지 않아도 되지만, 운영 데이터 보존과 실제 전환·복구 승인은 별도로 지켜야 한다.

## 목적

현재 운영 중인 Python Loki를 건드리지 않고 Go 후보판을 독립적으로 설치·검증·복구할 수 있게 만든다. 에이전트는 일반 개발 작업을 `devtools`와 셸로 수행하고, Loki는 비밀·브라우저·Git 서명·공유·격리 경계를 담당한다.

운영 전환은 이 계획을 통과한 후보 아티팩트에 대한 별도 작업이다. 구현과 검증은 현재 Python 서비스와 `/var/lib/loki/runtime`, `/etc/loki`, `/opt/loki-mcp`, `/opt/loki-browser`를 변경하지 않는다.

## 완료 기준

- 빈 Ubuntu 환경에 후보 아티팩트만으로 사용자, 디렉터리, 도구 체인, Chromium, systemd unit을 설치할 수 있다.
- 재부팅과 서비스 시작 지연 후에도 MCP가 필수 소켓을 기다렸다가 기동한다.
- 직접 실행한 devtools와 비밀 주입 프로세스가 같은 runner 환경·캐시·Git 설정을 사용한다.
- Node/npm/pnpm, Go, Python의 잠금 파일 기반 설치와 테스트가 runner 권한으로 동작한다.
- 프로젝트가 Playwright를 선언하면 그 버전의 브라우저를 runner 캐시에 설치하고 E2E 명령을 실행할 수 있다.
- Loki 브라우저는 후보판에 고정된 별도 Chromium을 쓰며 Python 배포 경로에 의존하지 않는다.
- 패키지 설치, 일반 런타임, 브라우저가 서로 다른 네트워크 정책을 사용한다.
- 비밀은 인자, 응답, 로그, devtools 상태, 프로젝트 파일에 남지 않는다.
- Python vault 복사본을 Go vault로 반복 가능하게 변환하고 원본 보존과 롤백을 검증한다.
- 설치 실패와 디스크·메모리 장애가 현재 Python 배포에 영향을 주지 않는다.

## 책임 경계

`devtools`는 프로젝트 탐색, 작업 큐, 등록된 workflow/action, 진단과 공개 상태를 맡는다. 프로젝트 제어 명령의 실행 수명주기는 Go MCP의 통합 `job` 도구가 담당하며, MCP는 비권한 executor에만 연결되고 executor가 별도의 privileged launcher를 통해 격리된 Job을 시작한다. 프로젝트 의존성 설치는 `dependency-install` 네트워크 프로필을 선택한 Job에서 수행한다. 번들에는 devtools와 Git 커밋, 검증, 계획, 조사 등 일반 에이전트 스킬을 함께 둔다.

Loki는 AES-GCM 비밀 저장·선택적 주입, Unix peer 인증, 감사, 비밀 누출 차단, workspace 경로 제한, Git 서명 키, CDP 브라우저, preview/artifact, 포트 소유권과 제한된 Docker 검사를 유지한다.

호스트 기준선은 Node/npm/pnpm, Go, Python, Git, CA, 기본 네이티브 빌드 도구와 브라우저 공유 라이브러리다. 프로젝트 라이브러리는 runner가 설치한다. 임의의 프로젝트가 호스트에서 `apt install`을 수행하지 않는다. 기준선 밖의 시스템 의존성은 제한된 일회성 컨테이너로 실행한다.

Loki 자체 브라우저 조작 검증은 Browser Plugin으로 수행한다. 이미 Playwright를 사용하는 프로젝트에는 그 프로젝트의 E2E 명령이 실행될 런타임과 캐시를 제공한다.

## 현재 상태

- Go 바이너리, 빌드 시 선택한 devtools, 고정 toolchain, Chromium, 전체 번들 스킬, unit과 설정을 checksum이 있는 후보 아티팩트로 만든다.
- root 비밀 상태와 runner의 설정·캐시·snapshot 경로가 분리되어 있으며 서비스 시작 시 소유권, 모드와 symlink 부재를 검사한다.
- 직접 devtools, MCP 도구와 비밀 주입 프로세스가 같은 폐쇄형 runner 환경과 관리자 Git 서명 정책을 사용한다.
- MCP는 runtime, port guard, browser, signing, executor socket을 기다린다. MCP는 launcher나 Docker socket에 직접 접근하지 않으며, executor가 privileged launcher에만 연결된다. native launcher unit은 Docker Unix socket 경로가 있어야 시작하지만 일반 lifecycle health는 Docker daemon의 실제 Job 실행 가능성을 증명하지 않는다. OCI 실행·network/endpoint 동작은 별도 supported-host acceptance fixture에서 검증한다.
- Python v1 vault 복사본을 반복 가능하게 가져오고 fingerprint, readback, backup과 원본 불변성을 검사한다.
- installer는 release 링크를 원자적으로 전환하고 health 실패 시 이전 release로 복귀한다.
- 격리 acceptance는 같은 후보로 두 번 clean install, upgrade, Chromium RPC, Git signing, reboot, rollback과 vault restore를 검증한다.

실제 명령과 장애 복구 절차는 [Go 후보 설치·검증·복구 Runbook](go-candidate-runbook.md)에 있다. 운영 전환은 승인된 후보를 대상으로 수행하는 별도 작업이다.

## 목표 구조

### 불변 기반

관리자 설치기는 버전·체크섬을 고정한 Go 바이너리, devtools, Loki Chromium, 스킬, helper, toolchain manifest를 `/opt/loki-go/<version>`에 설치한다. `/opt/loki-go/current`를 원자적으로 바꾸며 이전 버전 링크를 보존한다.

### 상태와 캐시

- root 전용: `/var/lib/loki-go/runtime`, `/var/lib/loki-go/signing`
- runner 전용 상태: `/var/lib/loki-go/runner*`, `/var/lib/loki-go/snapshots`
- runner 전용 캐시: `/var/cache/loki-go/runner*`
- 프로젝트 출력: `/workspace`
- 임시 파일: 작업별 `mktemp -d`와 종료 trap

installer 또는 `tmpfiles.d`가 정확한 UID/GID와 모드로 경로를 만든다. root runtime의 `StateDirectory`에는 비밀 상태만 둔다. 서비스는 시작할 때 소유권, 모드와 심볼릭 링크 부재를 검증한다.

### 단일 runner 환경

직접 devtools, 비밀 주입 프로세스, 설치 검증기가 하나의 allowlisted 환경 생성기를 쓴다. HOME, XDG, PATH, TMPDIR, Git, 언어별 캐시와 네트워크 정책을 명시하고 부모 환경을 상속하지 않는다.

의존성 설치에는 비밀을 주입하지 않는다. 잠금 파일과 실행 파일이 준비된 뒤 장기 실행 프로세스에만 선택한 비밀을 전달한다.

### 용도별 네트워크

- `dependency-install`: 고정된 registry와 artifact 출처
- `runtime-default`: localhost만 허용
- `runtime-profile`: 관리자가 선언한 서비스별 외부 호스트
- `browser`: public-web 프록시와 포트 정책

프록시는 최종 redirect 호스트도 검사한다. 허용 목록은 버전 관리되는 관리자 설정으로 두고 프로젝트가 바꿀 수 없게 한다.

### 브라우저 분리

Loki Chromium은 Loki 릴리스에 고정한다. Playwright 브라우저는 프로젝트의 Playwright 버전과 결합되므로 runner 캐시에 별도로 둔다. 프로젝트가 lockfile에 Playwright를 선언한 뒤 로컬 실행 파일로 설치·실행한다. 임시 `npx`가 최신 버전을 자동 설치하는 흐름은 검증 경로로 사용하지 않는다. OS 라이브러리는 기반 이미지 단계에서 root가 설치한다.

### 컨테이너 경계

기준선 밖의 서비스가 필요한 E2E는 rootless engine 또는 명령·mount·image·network를 제한한 broker로 실행한다. 일반 runner에게 raw Docker socket을 주지 않고 현재 Docker inspector와 분리한다.

## 구현 순서와 커밋

### 1. 실행 환경 계약 고정

- 산출물: 경로·UID/GID·모드·HOME/XDG/cache·네트워크 manifest와 실패 재현 테스트
- 부작용: 기존 runtime/layout fixture 변경
- 검증: 일반 사용자, root 가능 disposable Ubuntu, race
- 커밋: `test(runtime): define runner execution environment contract`

### 2. root와 runner 상태 분리

- 산출물: systemd StateDirectory 수정, runner state/cache 생성·검사, snapshot 이동
- 부작용: 미배포 Go 후보의 상태 경로 변경
- 검증: runner 쓰기, vault/key 읽기 거부, 재시작 유지, symlink 공격 거부
- 커밋: `fix(runtime): separate root vault and runner state`

### 3. 실행 환경 통합

- 산출물: 공통 environment builder와 devtools wrapper, Git/gh·언어별 캐시
- 부작용: 캐시 재생성으로 최초 실행 지연과 일시적 디스크 증가
- 검증: 모든 경로의 HOME·도구·Git·cache 일치, 부모 환경·비밀 비누출
- 커밋: `feat(runtime): unify runner environment across launch paths`

### 4. dependency egress 연결

- 산출물: 정책별 allowlist·프록시, registry fixture, timeout·redirect·audit
- 부작용: 허용한 공급망 호스트로 runner 외부 통신 가능
- 검증: 허용 출처 성공, 유사 도메인·다른 포트·우회·부적절한 redirect 실패
- 커밋: `feat(egress): add explicit dependency download profile`

### 5. 기반 도구 체인 설치

- 산출물: Ubuntu 24.04 package/version manifest, 설치기, `doctor`, provenance
- 부작용: 이미지 크기와 공격 표면 증가, 패키지 갱신이 릴리스 업무가 됨
- 검증: 빈 이미지 설치, 재설치 멱등성, 버전·체크섬, offline 재검증
- 커밋: `feat(packaging): provision pinned development toolchain`

### 6. 후보 전용 Chromium

- 산출물: Chromium 버전·체크섬·라이선스, 후보 경로와 소유권
- 부작용: 아티팩트 수백 MB 증가, 브라우저 보안 갱신 책임
- 검증: Python 경로 없는 환경 기동, Browser Plugin 탐색·다운로드·재시작
- 커밋: `feat(browser): package candidate-owned chromium`

### 7. 프로젝트 E2E 계약

- 산출물: lockfile 기반 Node fixture, runner browser cache, devtools process lifecycle, cache TTL·상한·잠금
- 부작용: Playwright 버전별 payload로 디스크 증가, 공유 캐시 오염 가능성
- 검증: clean-cache 설치, 재사용, offline 재실행, 동시 실행, 중단 정리, 비밀 비노출
- 커밋: `test(candidate): prove locked project e2e execution`

### 8. installer와 service lifecycle

- 산출물: stage/activate 분리, 사용자·그룹·소유권·unit 설치, readiness·health·복원
- 부작용: 별도 `/opt/loki-go`, `/etc/loki-go`, `/var/lib/loki-go`, `/var/cache/loki-go` 관리
- 검증: 반복 부팅, runtime 지연·충돌·강제 종료, 부분 실패, 재시도, 후보 롤백
- 커밋: `feat(packaging): install and activate isolated go candidate`

### 9. vault 마이그레이션

- 산출물: offline `loki migrate-vault`, 읽기 전용 복사본 입력, fingerprint/readback, backup·rollback
- 부작용: Go vault와 백업에 실제 비밀이 생겨 root 전용 보존·폐기 정책 필요
- 검증: 정상·손상·중단·반복·권한 오류·복원, source inode/digest 불변
- 커밋: `feat(state): expose recoverable legacy vault migration`

### 10. 후보 종합 검증

- 산출물: clean install → copied-state migration → boot → 기능 점검 → reboot → rollback, 결과표와 runbook
- 부작용: 네트워크·디스크·메모리·시간 사용 증가
- 검증: 같은 아티팩트가 깨끗한 환경에서 두 번 통과하고 Python 서비스와 상태 hash가 전후 동일
- 커밋: `docs(migration): record candidate acceptance and rollback runbook`

## 위험 통제표

| 위험 | 통제 |
|---|---|
| root 소유 HOME | root/runner 경로 분리와 부팅 시 모드 검사 |
| 실행 경로별 환경 차이 | 단일 환경 builder와 계약 테스트 |
| 공급망 확대 | lockfile, 비밀 없는 설치, allowlist, checksum/provenance |
| Playwright revision 불일치 | 프로젝트 로컬 버전과 별도 runner cache |
| 캐시·브라우저 디스크 증가 | 용량 상한, TTL, 잠금, 사용량 진단 |
| 외부 통신을 통한 비밀 유출 | 설치와 비밀 프로세스 분리, 용도별 egress |
| Docker socket의 root 권한 | raw socket 미노출, rootless 또는 제한 broker |
| vault 변환 실패 | 원본 불변, 복사본 입력, fingerprint/readback, 이중 백업 |
| 정리 작업 경합 | 작업별 temp, lease/lock, preview-then-apply |

## 작업 규칙

- 각 단계의 코드·테스트·문서를 한 행동 단위로 검증한 뒤 커밋한다.
- 4·7·10단계에서 race와 clean-environment 검증을 넓힌다.
- 저장소 `.tmp`에 생성물을 누적하지 않는다. 최종 후보만 보존하고 중간 디렉터리는 종료 시 정리한다.
- 기존 사용자 변경은 stage하지 않는다.
- 운영 전환 승인 전까지 현재 Python 배포에 설치·재시작·마이그레이션 명령을 실행하지 않는다.

## 외부 근거

- Playwright 브라우저 버전과 공유 캐시: <https://playwright.dev/docs/browsers>
- Playwright OS 의존성의 이미지 준비: <https://playwright.dev/docs/ci>
- systemd `StateDirectory=` 소유권: <https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html>
- Docker daemon 접근 권한: <https://docs.docker.com/engine/security/>

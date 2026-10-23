# Releaseway 업데이트와 생성 노트

사용자 추가 요청으로 v0.4.1 → v0.8.0 (`b6f17f2ffe2c8fd92bdf6be4729e1e56d151bef3`)를 고정했다. [공식 릴리스](https://github.com/releaseway/actions/releases/tag/v0.8.0)와 해당 ref의 action.yml/publishing.md/src/action.ts를 확인했다. composite가 Node24를 설정하며 기존 tag/commit/assets/upload-concurrency/title/latest 계약은 유지된다.

동일 본문 원인은 Releaseway 미사용이 아니라 고정 설명 파일을 notes:file로 전달한 것이다. notes:standard로 Conventional Commit 기반 분류·비교 링크를 생성한다. notes-existing:auto는 새 릴리스와 draft의 본문을 검증하고 이미 게시된 immutable 릴리스 본문은 재생성 없이 보존한다. 오래된 공개 본문과 새 템플릿의 충돌을 피하면서 recovery 원본 bytes 계약을 유지한다.

검증: 격리된 upstream checkout에서 frozen lockfile/ignore-scripts 설치 후 지원 Node24.22.0으로 notes/action-smoke/lifecycle/range 54개 통과. 처음 실행은 테스트 의존성 미설치로 실패했고, 잠금 설치 후 통과했다. Loki workflow 계약 3개 통과. 실제 v0.2.21 tag를 dist/main.cjs notes-preview:true로 실행해 state=preview, notes-state=prepared, 빈 release-url을 확인했다. 공개 릴리스나 tag는 변경하지 않았다.

리뷰 1·2: 지원 입력·고정 SHA·표준 분류·게시 전 검증·draft 재개·기존 본문 보존·권한·비공개 토큰 전달 경계를 확인했다. 범위 내 남은 결함 없음. 실제 게시 관측은 별도 승인된 릴리스에서 확인해야 한다.

추가 조사: [issue #3](https://github.com/jinyongp/loki/issues/3)은 2026-10-19의 의존성 제안 15건이다. 2026-10-23T18:23Z 현재 watcher dry-run은 검토 20건, unavailable 0건이다. Chrome 156.0.8078.47, Node26.12.1, chrome-devtools-mcp1.12.0으로 후보가 전진했고 Go1.27.3/BuildKit/Buildx/gh/Git 후보가 추가됐다. devtools는 현재 pin 반영으로 기존 제안이 사라졌다. download-artifact는 일부 별도 workflow에 옛 SHA가 남아 제안이 유지된다. 본 issue는 자동 추적용이라 미해결 상태가 정상이며, 이 조사만으로 전체 dependency 업데이트·issue 수정/닫기를 수행하지 않았다.

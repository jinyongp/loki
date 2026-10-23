# 기존 전체 릴리스 제목과 노트

사용자의 “지금까지 생성된 모든 릴리즈 전부 … title도” 요청에 따라 GitHub release title/body 변경을 승인된 범위로 처리했다. 대상은 v0.1.0–v0.1.52, v0.2.1–v0.2.21의 전체 74개다. GitHub connector에 release 수정 도구가 없어 gh REST API를 사용한다.

Releaseway v0.8.0의 dist/main.cjs를 notes:standard, notes-preview:true로 각 실제 tag/commit에 실행했다. 제목은 실제 해당 변경의 핵심을 반영한 `Loki vVERSION · 변경 요약`으로 개별 작성했다. 본문은 upstream 표준 템플릿의 카테고리·commit 링크·전체 비교 링크를 그대로 사용한다. 고정 설치 설명을 릴리스마다 복제하지 않는다.

리뷰 1: 전체 74개 target SHA와 생성 범위, non-merge commit 누락·중복, 실제 commit 링크 집합, 본문 길이/hash, 제목 버전·길이를 독립 git 조회와 대조했다. 본문은 74개 모두 서로 다르다. v0.1.0 첫 릴리스의 전체 이전 이력 포함은 standard의 first-release:all 계약이다.

리뷰 2: 이전 title/body 및 asset 목록을 작업용 원본 snapshot으로 보관했다. PATCH body는 name/body 두 필드만 포함했다. 저장 결과를 별도 GET으로 다시 읽고 asset ID/이름/크기/digest, tag name, draft/prerelease/published_at을 전후 대조한다. 전체 remote tag 목록도 전후 대조한다. v0.1.52의 최초 remote-binding 일시 실패는 실제 remote ref를 확인한 뒤 해당 1개만 다시 preview하여 통과했다. 갱신 중 연결 timeout 2건은 해당 저장 상태를 읽고 이미 완료된 항목을 재작성하지 않는 방식으로 복구했다.

의도 외 응답: v0.1.0은 원래 immutable:false였으나 name/body PATCH 후 GitHub 응답에서 true가 됐다. 이 1개는 “모든 flag 불변” 검사를 통과했다고 기록하지 않는다. 요청 필드에는 draft·tag·immutable을 넣지 않았다. 제목·본문은 요청대로 저장됐고 asset/tag 및 공개 상태는 원본과 대조한다. 원인을 저장소 정책으로 확정하지 않았으며, 이를 되돌리려고 릴리스 삭제나 tag/asset 변경을 시도하지 않았다. [GitHub의 immutable release 계약](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)은 title/notes 편집은 허용하고 asset/tag 보호를 구분한다.

실제 적용 결과는 같은 디렉터리의 P07-release-metadata-results.json에 기록한다. 과거 workflow에 고정된 notes:file/명시적 옛 제목으로 역사적 게시를 다시 검증하면 새 공개 본문과 다르므로 실패할 수 있다. 이번 변경은 과거 immutable asset/tag를 재생성하거나 과거 commit의 workflow를 수정하지 않는다. 앞으로의 새 workflow는 standard 생성과 notes-existing:auto를 사용한다.

최종 검증: 74개 모두 title/body 정확한 저장을 별도 GET으로 확인했다. asset/tag 변경 0개, latest v0.2.21 유지, published/draft/prerelease 상태 유지. immutable flag 차이는 v0.1.0의 false→true 1건이며 결과 JSON에 명시했다. 원격 쓰기 이후 추가 수정은 수행하지 않았다.

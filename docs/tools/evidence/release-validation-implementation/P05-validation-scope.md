# 통합 검증 실행 경계

사용자 요청한 native CI는 codex/release-validation-optimization에서 workflow_dispatch, publish=false, force_rebuild=true, expected_sha=정확한 HEAD로 실행한다. main-only 조건은 publish=true에 유지하며 publisher의 main SHA 재확인도 유지한다. branch에서는 공유 registry build cache 쓰기를 끈다. 후보 이미지 업로드와 Actions artifact/cache는 검증에 필요한 중간 산출물이다. 공개 release/tag/Pages 게시를 수행하지 않는다.

리뷰: 비공개 검증과 공개 게시 guard를 분리하고, 기본 publish=false와 gate의 실제 byte 검증을 확인했다. 실제 실행 결과는 P05 완료 기록에서 별도로 남긴다.

추가 사용자 요청의 Releaseway 최신화·standard 템플릿·issue #3 조사는 P06-releaseway-review.md와 별도 커밋으로 기록했다. 기존 P01–P04의 byte/증거/권한/복구 계약과 native 수용 조건은 유지한다.

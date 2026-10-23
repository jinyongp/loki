# R04 전송·봉인 대안과 리뷰

## 공식 계약과 pinned 소스

2026-10-24 KST에 GitHub 공식 [job 의존성](https://docs.github.com/en/actions/how-tos/write-workflows/choose-what-workflows-do/use-jobs), [재실행](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs), [artifact 전달](https://docs.github.com/en/actions/tutorials/store-and-share-data)을 읽었다. 실패/skip 전파는 status 조건으로 제어해야 하며, 재실행은 원래 SHA/ref와 원래 actor 권한을 사용한다. retry를 새로운 소스 승인으로 취급할 수 없다.

사용 중인 download-artifact 9000827ccba6bdab643e8b6fd33ac0654aef8333의 src/download-artifact.ts와 action.yml을 확인했다. `name`이 없는 artifact는 오류지만, pattern 조회의 빈 결과는 download promise 0개로 끝난다. `name`이 있으면 pattern은 무시된다. digest-mismatch 기본값은 error이며 실제 실패 분기도 소스에서 확인했다. merge-multiple 기본 false이고 매칭 수에 따라 디렉터리 배치가 달라질 수 있어 단순 새 디렉터리 가정으로 파일을 찾으면 안 된다. 기본 repository/run은 현재 실행이다.

upload-artifact cf430e030ddbb5b0abf93d22962f4752f3646cd9 README의 immutable artifact 계약과 덮어쓰기 동작을 확인했다. 기존 이름을 무작정 다시 업로드하거나 merge하는 구조를 추가하지 않는다. Releaseway 5841e545ff259790a3b2461299e741d62c69a73d의 action → src/action.ts → scripts/release.sh 호출을 따라갔다. script는 로컬 파일의 SHA-256 목록을 만들고 원격 asset의 정확한 이름/상태/digest를 검사한다. draft 재개와 existing immutable 릴리스 검증이 분리되며 게시 전에 remote tag를 재확인한다. 로컬 glob에 원치 않는 파일이 섞이면 그것도 기대 파일이 되므로 Loki가 게시 입력의 정확한 집합을 먼저 확인해야 한다.

소스 URL·commit·Git blob SHA·내용은 pinned-upstream-sources.json과 releaseway-execution-sources.json에 보관했다. upstream-licenses.json에 해당 commit의 라이선스를 포함했다. releaseway-source-index.json은 조회한 src 파일 목록이다. 이 확인은 upstream 코드를 실행한 증거가 아니다.

## 비교 및 결정

| 대안 | 전송·권한 | 결정 |
| --- | --- | --- |
| A: 소비자별 artifact family 축소 | native 검사에서 약 6.62GB의 무관한 전송 제거 투영. 현재 gate/publish 권한 유지 | 첫 구현 단계로 채택 |
| B: 조립 inventory → 가벼운 gate → 게시 전 byte 확인 | publish=true에서 gate의 3.73GB 추가 수신 제거 가능. publisher의 기존 읽기와 검증은 유지. publish=false는 gate가 한 번 실제 bytes 확인 | 별도 두 번째 구현 단계의 설계로 채택. 첫 단계와 독립 검증·되돌리기 필요 |
| C: 최종 검증과 게시를 같은 job으로 합치기 | job 단위 contents:write가 검증 단계에도 적용되거나 gate/publish의 현재 분리가 바뀜 | 권한 분리 보존 조건에 불리해 기각. 실행 prototype 불필요 |

A는 native matrix에 build 여부를 명시하고 필요한 family를 정확한 artifact 이름으로 다운로드하는 방식이다. rebuild로 선택된 family는 `name` 조회로 누락 시 실패해야 한다. 재사용되어 이번 run에 artifact가 없는 family는 다운로드 step을 생략한 뒤 기존 materialize의 검증된 공개 URL/길이/digest 경로를 사용한다. manager: manager만, browser: manager+project-host browser, full: manager+full만 받는다. full 안의 개별 파일을 부분 다운로드하는 기능으로 가정하지 않는다. 새 built-*가 없는 합법적인 check-only 실행과 필요한 새 산출물 누락을 구분한다.

B의 최종 설계는 R05 전략의 byte 봉인 절차를 따른다. 조립 전 lock 증거와 최종 lock을 구분하고, public payload 바깥의 inventory로 final file set·길이·SHA-256을 고정한다. gate 판정은 읽기 전용으로 유지한다. publish=true는 publisher에서 같은 검증 함수로 실제 파일을 모두 확인한 후에만 tag/Releaseway를 호출한다. publish=false는 gate에서 실제 파일 확인을 반드시 수행한다. 따라서 검증 전용 실행에서 byte 검증이 사라지지 않는다. 기존 exact-source public recovery는 원래 봉인된 바이트를 그대로 사용한다.

## 실험과 한계

실제 release_gate/materialize 함수의 상태·증거·digest·재사용 경계는 R02의 contract_experiments.py 결과를 재사용한다. 다른 source/run, 미래 attempt, 잘못된 lock, 누락 증거, 필수 실패/취소, unselected 취소를 거부했고 같은 run 과거 attempt와 원래 sealed lock 유지 경로를 확인했다.

추가 transfer_model.py 4개 unittest는 publish=false에서도 byte 변조를 거부하고 외부 효과가 없음을 확인한다. publish=true의 파일 누락/추가/변조, 조립 전 proof lock 불일치, 최종 lock 변조를 거부하며 반복/공개 recovery에서 최종 바이트를 바꾸지 않는다. 이 모형은 새 제품 구현이나 실제 Actions 수용이 아니다. 기존 gate의 모든 파일 의미를 새 코드로 옮기는 작업과 실제 native/CI/Releaseway 실행은 구현 단계의 필수 완료 조건이다.

리뷰루프에서는 pattern 빈 결과를 새 build 성공으로 오인할 수 있는 점을 발견해 선택된 build에는 exact name+명시 guard를 사용하도록 정했다. 게시되는 local glob이 기대 파일 목록을 스스로 확장하지 않도록 final file-set 비교도 필수로 정했다. rejected C는 불필요하게 실험하지 않았다. 격리 모델과 현재 pinned 소스 근거를 대조한 후 추가 계획 결함은 없었다. 실제 시간 개선과 공개 게시/복구 통과는 주장하지 않는다.

# R05 최종 전략 리뷰

범위는 R01–R04의 연구 근거에서 최적화 전략을 확정하는 일이다. 제품 변경·native CI·공개 전환은 후속 구현의 완료 조건으로 남긴다. devtools task ID·수용 기준·요구사항을 바꾸지 않았다.

| 검토 영역 | 확인 결과 |
| --- | --- |
| 비용·선택 기준 | historical 분포와 matched 성능을 구분하고 사전 decision-rules를 유지함. 6.62GB는 구조 투영이며 시간 성과가 아님 |
| 검증 동등성 | 실제 retained callback의 정상/대표 결함 대조가 있고 installer 연결·OS·race·full 경계는 유지함 |
| 입력·재사용 | unknown fallback을 보류하고 build 없는 check-only/reuse와 새 build 누락을 구분함 |
| 획득·토폴로지 | exact name의 missing 실패, pattern 빈 성공, family 디렉터리·중복 파일을 명시함 |
| 봉인 | 조립 전 lock → accepted lock → SHA256SUMS → inventory 순서에 자기 hash 순환이 없음 |
| 게시·권한 | gate 읽기/publisher 쓰기를 분리하고 실제 byte 확인 전에 tag/Releaseway를 호출하지 않음 |
| 검증 전용 | publish=false는 gate에서 같은 실제 byte verifier를 실행하며 게시 효과가 없음 |
| 복구 | 원래 immutable 공개 바이트 유지, partial retry/다른 source·run/미래 attempt 거부를 명시함 |
| 실험·근거 | native 미실행·로컬 Go 버전 차이·모형 범위를 표시하고 upstream snapshot의 Git blob과 라이선스를 확인함 |
| 적용·되돌리기 | 독립 작업별 리뷰루프·커밋, P04 revert의 기존 byte 검사 복원, 별도 공개 전환 조건이 있음 |

리뷰에서 새 build 누락을 공개 fallback으로 덮는 위험, publish=false의 검증 누락, inventory와 checksum의 순환, 실제 CLI-only baseline 조건을 재검토해 전략에 명시했다. 소스 snapshot·local 실험을 실제 플랫폼 수용으로 표현하지 않았는지 다시 확인했다. 문서 링크·JSON·파일 hash와 제품 소스 무변경도 확인했다. 계획 범위의 추가 미해결 지적은 없다. 이는 후속 구현이나 배포 검증의 통과 선언이 아니다.

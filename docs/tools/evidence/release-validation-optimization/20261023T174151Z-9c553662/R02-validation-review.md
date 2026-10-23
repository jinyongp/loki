# R02 검증 영역과 리뷰

기준 제품 소스: 4f1d66fe202e125d2354250d10a655462bd3d66c. 배포 source/race, native manager·upgrade·installer·system-host, browser 기본/optional·실제 저장 연결·최소 Linux 배포판, full workspace/composition, gate/publisher/public verify 영역을 비교했다. 단위 테스트 전체를 삭제 후보로 분류한 것은 아니다.

독립 browser 기본 호출은 capabilities의 동일 accept()/기본 protocol 호출과 겹친다. `contract_experiments.py`는 실제 기존 callback에 process/network 대체물을 주입했다. 정상 대조와 9가지 대표 결함에서 독립 기본 검사와 retained callback의 오류가 같았다. 별도 installer가 생성한 연결 실행은 다른 경계이므로 유지한다. 기존 race와 CI 부분 race도 범위가 달라 유지한다.

총 6개 unittest에는 기본 프로토콜 동등성, 실제 materialize의 새 manager/공개 재사용 및 corrupt 로컬 파일 거부, 실제 gate의 증거/상태 거부·같은 run 과거 attempt·동일 버전 recovery, 독립적인 미래 inventory 모형이 들어 있다. inventory 모형은 제품 구현이 아니다. 실제 제품·브라우저·Docker·installer를 실행하지 않았다. 실제 native 5개 browser target 확인은 후속 구현의 필수 조건이다.

리뷰루프: 양성 대조 없이 실패만 같다고 판정하지 않는지, 결함이 정확한 프로토콜 경계에 도달하는지, retained optional 분기까지 통과했다고 과장하지 않는지, 가짜 외부 경계 외에 자식 프로세스가 실행되지 않는지 검토했다. 추가 수정이 필요한 결함은 없었다. 결과 JSON은 최초 실행 기록이다. 보관 결과를 덮어쓰지 않는 재검증 명령은 `python3 -m unittest discover -s docs/tools/evidence/release-validation-optimization/20261023T174151Z-9c553662 -p contract_experiments.py`다.

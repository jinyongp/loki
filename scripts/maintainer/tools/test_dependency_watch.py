"""Release independence, advisory policy and bounded issue lifecycle contracts."""
import copy
from datetime import date
import unittest
from unittest.mock import patch

import dependency_inventory as inventory
import dependency_watch as watch
import verify_release_tools


def item(key='chrome', kind='chrome', current='1.0.0'):
    return dict(id=key, kind=kind, current=[current], source='fixture', paths=['fixture'])


class Source:
    def __init__(self, version='1.1.0', fail=False):
        self.version, self.fail = version, fail

    def candidate(self, dependency):
        if self.fail:
            raise TimeoutError('fixture unavailable')
        return dict(version=self.version, url='https://example.org/release/'+self.version)


def report(version='1.1.0', fail=False, current='1.0.0'):
    return watch.collect([item(current=current)], {'holds': {}}, Source(version, fail), date(2026, 10, 14))


class Issues:
    def __init__(self):
        self.items, self.calls = [], []

    def issues(self):
        return copy.deepcopy(self.items)

    def create(self, body):
        self.calls.append(('create',))
        issue = dict(number=len(self.items)+1, body=body, state='open', html_url='https://example.org/issue/1')
        self.items.append(issue)
        return issue

    def update(self, number, data):
        self.calls.append(('update', number, data))
        next(issue for issue in self.items if issue['number']==number).update(data)


class PinContracts(unittest.TestCase):
    def test_release_pin_verification_never_queries_upstream(self):
        with patch('urllib.request.urlopen', side_effect=AssertionError('release queried upstream')), patch('subprocess.run', side_effect=AssertionError('release invoked an external lookup')):
            verify_release_tools.verify()

    def test_changed_receipt_is_rejected_without_a_latest_version_check(self):
        original = verify_release_tools.read
        def read(path):
            value = original(path)
            if path.name == 'browser-linux-amd64-project-host.json':
                value['assets']['chrome']['version'] = '999.0.0.0'
            return value
        with patch.object(verify_release_tools, 'read', side_effect=read):
            with self.assertRaisesRegex(ValueError, 'receipt'):
                verify_release_tools.verify()

    def test_inventory_covers_all_workflow_pins_and_native_tools(self):
        items = {row['id']: row for row in inventory.inventory()}
        self.assertIn('tool:git:amd64', items)
        self.assertIn('ffmpeg', items)
        self.assertIn('gomod:github.com/modelcontextprotocol/go-sdk', items)
        action = items['action:actions/download-artifact']
        self.assertGreater(len(action['current']), 1)
        self.assertIn('.github/workflows/browser-final-inspection.yml', action['paths'])
        self.assertEqual(items['node']['current'], [inventory.read(inventory.ROOT/'package.json')['engines']['node']])


class AdvisoryPolicy(unittest.TestCase):
    def test_holds_expire_and_do_not_hide_the_next_candidate(self):
        rules = {'holds': {'chrome': {'version': '1.1.0', 'reason': 'native regression'}}}
        rows = watch.collect([item()], rules, Source(), date(2026, 10, 14))['rows']
        self.assertEqual(rows[0]['status'], 'held')
        self.assertEqual(watch.collect([item()], rules, Source('1.2.0'))['rows'][0]['status'], 'review')
        rules['holds']['chrome'] = {'until': '2026-10-14', 'reason': 'release cycle'}
        self.assertEqual(watch.collect([item()], rules, Source(), date(2026, 10, 14))['rows'][0]['status'], 'held')
        self.assertEqual(watch.collect([item()], rules, Source(), date(2026, 10, 15))['rows'][0]['status'], 'review')

    def test_downgrades_prereleases_and_failures(self):
        self.assertEqual(report('0.9.0')['rows'][0]['status'], 'current')
        self.assertEqual(report('2.0.0-beta.1')['rows'][0]['status'], 'current')
        self.assertEqual(report('v1.0.0')['rows'][0]['status'], 'current')
        self.assertEqual(report(fail=True)['rows'][0]['status'], 'unknown')

    def test_debian_updates_remain_on_the_selected_distribution(self):
        accepted = item('tool:git:amd64', 'ubuntu-package', '1:2.43.0-1ubuntu7.3')
        newer = watch.collect([accepted], {'holds': {}}, Source('1:2.43.0-1ubuntu7.4'))['rows'][0]
        self.assertEqual(newer['status'], 'review')
        self.assertEqual(watch.collect([accepted], {'holds': {}}, Source('1:2.43.0-1ubuntu7.2'))['rows'][0]['status'], 'current')

    def test_action_candidate_resolves_an_annotated_tag(self):
        class Metadata(watch.Upstream):
            def github(self, path):
                if path.endswith('/releases/latest'):
                    return dict(draft=False, prerelease=False, tag_name='v6.0.0', html_url='https://github.com/actions/checkout/releases/tag/v6.0.0')
                if '/git/ref/' in path:
                    return {'object': {'type': 'tag', 'sha': 'a'*40}}
                return {'object': {'type': 'commit', 'sha': 'b'*40}}
        candidate = Metadata().candidate(item(kind='action'))
        self.assertEqual(candidate['version'], 'b'*40)
        self.assertEqual(candidate['release'], 'v6.0.0')


class IssueLifecycle(unittest.TestCase):
    def test_one_issue_noop_close_reopen_and_supersede(self):
        github = Issues()
        self.assertEqual(watch.publish(report('1.0.0'), github)['action'], 'none')
        self.assertEqual(watch.publish(report(), github)['action'], 'created')
        first = github.items[0]['number']
        github.calls.clear()
        self.assertEqual(watch.publish(report(), github)['action'], 'unchanged')
        self.assertEqual(github.calls, [])
        github.items[0]['body'] += '\nManual investigation notes\n'
        watch.publish(report('1.2.0'), github)
        self.assertNotIn('1.1.0', github.items[0]['body'])
        self.assertIn('Manual investigation notes', github.items[0]['body'])
        watch.publish(report('1.2.0', current='1.2.0'), github)
        self.assertEqual(github.items[0]['state'], 'closed')
        watch.publish(report('1.3.0', current='1.2.0'), github)
        self.assertEqual(github.items[0]['state'], 'open')
        self.assertEqual([issue['number'] for issue in github.items], [first])

    def test_partial_failure_preserves_last_candidate_and_open_issue(self):
        github = Issues()
        watch.publish(report(), github)
        watch.publish(report(fail=True), github)
        self.assertEqual(github.items[0]['state'], 'open')
        self.assertIn('1.1.0', github.items[0]['body'])
        self.assertIn('unknown', github.items[0]['body'])
        github.calls.clear()
        watch.publish(report(fail=True), github)
        self.assertEqual(github.calls, [])
        empty = Issues()
        self.assertEqual(watch.publish(report(fail=True), empty)['action'], 'none')
        self.assertEqual(empty.items, [])

    def test_held_candidates_do_not_spawn_new_issues(self):
        github = Issues()
        held = watch.collect([item()], {'holds': {'chrome': {'version': '1.1.0', 'reason': 'regression'}}}, Source())
        self.assertEqual(watch.publish(held, github)['action'], 'none')
        watch.publish(report(), github)
        watch.publish(held, github)
        self.assertEqual(github.items[0]['state'], 'closed')
        self.assertIn('regression', github.items[0]['body'])

    def test_owned_duplicate_notes_are_kept_before_closing(self):
        github = Issues()
        watch.publish(report(), github)
        duplicate = copy.deepcopy(github.items[0])
        duplicate.update(number=2, body=duplicate['body']+'\nOther maintainer notes\n')
        github.items.append(duplicate)
        watch.publish(report(), github)
        self.assertEqual(github.items[1]['state'], 'closed')
        self.assertIn('Other maintainer notes', github.items[0]['body'])
        github.calls.clear()
        watch.publish(report(), github)
        self.assertEqual(github.calls, [])
        self.assertEqual(github.items[0]['body'].count('Other maintainer notes'), 1)

    def test_human_issue_is_not_owned_by_marker_alone(self):
        class API(watch.GitHubIssues):
            def api(self, path, data=None, method=None):
                return [dict(number=1, body=watch.START, user={'login': 'maintainer'}),
                        dict(number=2, body=watch.START, user={'login': watch.BOT})]
        self.assertEqual([issue['number'] for issue in API('fixture/repo').issues()], [2])

    def test_malformed_managed_state_refuses_to_overwrite_human_notes(self):
        github = Issues()
        watch.publish(report(), github)
        github.items[0]['body'] += '\n'+watch.START+'\n'
        before = copy.deepcopy(github.items)
        with self.assertRaisesRegex(ValueError, 'Ambiguous'):
            watch.publish(report(), github)
        self.assertEqual(github.items, before)


if __name__ == '__main__':
    unittest.main()

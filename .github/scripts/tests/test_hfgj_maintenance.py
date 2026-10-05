import copy
import gzip
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import zipfile

SCRIPT = Path(__file__).resolve().parents[1] / 'hfgj-release.py'
spec = importlib.util.spec_from_file_location('maintenance', SCRIPT)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def metadata():
    return dict(repository=m.REPOSITORY, channel=m.CHANNEL, base_sha='2'*40,
                mirror_sha='3'*40, upstream_sha='4'*40, upstream_tag_sha='4'*40,
                source_sha='1'*40, upstream_tag='v1.2.3', upstream_is_tag=True,
                version='v1.2.3-hfgj.'+'1'*12, build_time='2026-10-05T00:00:00Z',
                changed=True, release_needed=True)


def make_packages(directory, meta):
    directory.mkdir()
    for base, (extension, kind, machine) in m.TARGETS.items():
        binary = bytearray(128)
        if kind == 'elf':
            binary[:6] = b'\x7fELF\x02\x01'
            binary[18:20] = machine.to_bytes(2, 'little')
            binary[32:40] = (64).to_bytes(8, 'little')
            binary[54:56] = (56).to_bytes(2, 'little')
            binary[56:58] = (1).to_bytes(2, 'little')
            binary[64:68] = (1).to_bytes(4, 'little')
        elif kind == 'pe':
            binary[:2] = b'MZ'; binary[60:64] = (64).to_bytes(4, 'little')
            binary[64:68] = b'PE\0\0'; binary[68:70] = machine.to_bytes(2, 'little')
        else:
            binary[:4] = b'\xcf\xfa\xed\xfe'; binary[4:8] = machine.to_bytes(4, 'little')
        binary += meta['version'].encode() + b' https://github.com/hfgj/mihomo/releases/download/HFGJ-Stable/'
        path = directory / f"{base}-{meta['version']}.{extension}"
        if extension == 'zip':
            with zipfile.ZipFile(path, 'w', zipfile.ZIP_DEFLATED) as archive:
                archive.writestr(base+'.exe', binary)
        else:
            with path.open('wb') as target, gzip.GzipFile(filename=base, fileobj=target, mode='wb', mtime=0) as stream:
                stream.write(binary)
    return m.assemble(directory, meta)


class FakeGitHub:
    def __init__(self, meta, failure=None):
        self.source = meta['source_sha']; self.tag = meta['base_sha']
        self.body = 'previous release'; self.failure = failure; self.mutations=[]
        self.assets={}; self.next_id=1
        self.add('version.txt', b'v1.2.2-hfgj.222222222222\n')
        self.add('old-core.gz', b'old archive')
        sums=''.join(m.digest(self.assets[n]['data'])+'  '+n+'\n' for n in ('version.txt','old-core.gz'))
        self.add('SHA256SUMS', sums.encode())
        self.old_version=self.assets['version.txt']['data']; self.old_sums=self.assets['SHA256SUMS']['data']

    def add(self, name, data):
        asset=dict(id=self.next_id, name=name, data=data, digest='sha256:'+m.digest(data))
        self.next_id+=1; self.assets[name]=asset
        return copy.deepcopy({k:v for k,v in asset.items() if k!='data'})

    def api(self, path, method='GET', data=None):
        if method=='GET':
            if path=='/git/ref/heads/hfgj': return {'object':{'sha':self.source}}
            if path=='/git/ref/tags/HFGJ-Stable': return {'object':{'sha':self.tag}}
            if path=='/releases/tags/HFGJ-Stable':
                return dict(id=7, assets=[{k:v for k,v in a.items() if k!='data'} for a in self.assets.values()],
                            body=self.body, upload_url='fake-upload')
            raise AssertionError(path)
        self.mutations.append((method,path,data))
        if path.startswith('/releases/assets/'):
            asset=next(a for a in self.assets.values() if a['id']==int(path.rsplit('/',1)[-1]))
            name=asset['name']
            if method=='DELETE': del self.assets[name]; return None
            new=data['name']
            if self.failure=='rename-version' and new=='version.txt':
                self.failure=None; raise RuntimeError('injected rename failure')
            del self.assets[name]; asset['name']=new; self.assets[new]=asset
            return copy.deepcopy({k:v for k,v in asset.items() if k!='data'})
        if path=='/git/refs/tags/HFGJ-Stable': self.tag=data['sha']; return None
        if path=='/releases/7': self.body=data['body']; return None
        raise AssertionError(path)

    def upload(self, release, name, data):
        self.mutations.append(('upload',name))
        if self.failure=='upload' and name.endswith('.zip'):
            self.failure=None; raise RuntimeError('injected upload failure')
        if name in self.assets: raise AssertionError('duplicate upload name')
        return self.add(name,data)

    def download(self, asset, *, cache_key=None):
        return self.assets[asset['name']]['data']


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(); self.root=Path(self.temp.name)
        self.meta=metadata(); self.packages=self.root/'packages'
        make_packages(self.packages,self.meta)
        self.client=FakeGitHub(self.meta)

    def tearDown(self): self.temp.cleanup()

    def run_publish(self): return m.publish(self.client,self.packages,self.meta,self.root/'recovery')

    def assert_old_channel(self):
        self.assertEqual(self.client.assets['version.txt']['data'],self.client.old_version)
        self.assertEqual(self.client.assets['SHA256SUMS']['data'],self.client.old_sums)
        self.assertEqual(self.client.tag,self.meta['base_sha'])
        self.assertEqual(self.client.body,'previous release')
        self.assertEqual(self.client.assets['old-core.gz']['data'],b'old archive')

    def test_success_retains_old_archive_and_changes_version_last(self):
        self.assertEqual(self.run_publish()['status'],'published')
        self.assertIn('old-core.gz',self.client.assets)
        self.assertEqual(self.client.tag,self.meta['source_sha'])
        renames=[x[2]['name'] for x in self.client.mutations if x[0]=='PATCH' and x[1].startswith('/releases/assets/')]
        self.assertEqual(renames,['SHA256SUMS','version.txt'])
        self.assertEqual((self.root/'recovery/version.txt').read_bytes(),self.client.old_version)

    def test_upload_failure_preserves_old_channel(self):
        self.client.failure='upload'
        with self.assertRaisesRegex(RuntimeError,'injected'): self.run_publish()
        self.assert_old_channel()

    def test_pointer_failure_restores_old_metadata(self):
        self.client.failure='rename-version'
        with self.assertRaisesRegex(ValueError,'previous channel metadata restored'): self.run_publish()
        self.assert_old_channel()

    def test_rename_propagation_delay_succeeds_without_rollback(self):
        original=self.client.download; counts={}
        def lagged(asset, **kwargs):
            name=asset['name']; counts[name]=counts.get(name,0)+1
            data=original(asset,**kwargs)
            if kwargs and name in ('SHA256SUMS','version.txt') and counts[name]<=3:
                return b'stale cached data'
            return data
        with patch.object(self.client,'download',side_effect=lagged), patch.object(m.time,'sleep'):
            self.assertEqual(self.run_publish()['status'],'published')
        self.assertFalse((self.root/'recovery/failure.json').exists())
        self.assertEqual(self.client.tag,self.meta['source_sha'])

    def test_persistent_version_staleness_restores_and_records_cause(self):
        original=self.client.download
        def stale(asset, **kwargs):
            data=original(asset,**kwargs)
            if asset['name']=='version.txt' and data!=self.client.old_version:
                return self.client.old_version
            return data
        with patch.object(self.client,'download',side_effect=stale), patch.object(m.time,'sleep'):
            with self.assertRaisesRegex(ValueError,'previous channel metadata restored'): self.run_publish()
        self.assert_old_channel()
        diagnostic=json.loads((self.root/'recovery/failure.json').read_text())
        self.assertEqual(diagnostic['phase'],'replace-version')
        self.assertEqual(diagnostic['original_error']['cause']['message'],'Uploaded asset bytes differ')
        self.assertEqual(diagnostic['recovery_errors'],[])

    def test_recovery_http_error_preserves_original_diagnostic(self):
        self.client.failure='rename-version'; original=self.client.api
        def failing(path, method='GET', data=None):
            if method=='PATCH' and path=='/git/refs/tags/HFGJ-Stable' and data['sha']==self.meta['base_sha']:
                raise urllib.error.HTTPError('https://example.invalid/secret',403,'private-detail',None,None)
            return original(path,method,data)
        with patch.object(self.client,'api',side_effect=failing):
            with self.assertRaisesRegex(ValueError,'recovery incomplete'): self.run_publish()
        diagnostic=json.loads((self.root/'recovery/failure.json').read_text())
        self.assertEqual(diagnostic['original_error']['type'],'RuntimeError')
        self.assertEqual(diagnostic['recovery_errors'][0]['error'],{'type':'HTTPError','status':403})
        self.assertNotIn('private-detail',json.dumps(diagnostic))

    def test_stale_source_refuses_all_writes(self):
        self.client.source='9'*40
        with self.assertRaisesRegex(ValueError,'advanced'): self.run_publish()
        self.assertEqual(self.client.mutations,[])
        self.assert_old_channel()

    def test_same_asset_name_different_bytes_is_refused(self):
        name=next(iter(json.loads((self.packages/'maintenance-manifest.json').read_text())['packages']))
        self.client.add(name,b'different bytes')
        with self.assertRaisesRegex(ValueError,'digest differs'): self.run_publish()
        self.assertEqual(self.client.mutations,[])
        self.assert_old_channel()

    def test_changed_artifact_is_refused_before_any_write(self):
        path=next(self.packages.glob('*.gz')); path.write_bytes(b'bad')
        with self.assertRaises(ValueError): self.run_publish()
        self.assertEqual(self.client.mutations,[])

    def test_idempotent_publish_does_not_mutate_channel(self):
        self.run_publish(); self.client.mutations=[]
        result=m.publish(self.client,self.packages,self.meta,self.root/'second-recovery')
        self.assertEqual(result['status'],'unchanged')
        self.assertEqual(self.client.mutations,[])

    def test_same_version_missing_package_is_refused(self):
        self.run_publish(); self.client.mutations=[]
        del self.client.assets[next(n for n in self.client.assets if n.endswith('.zip'))]
        with self.assertRaisesRegex(ValueError,'missing channel packages'):
            m.publish(self.client,self.packages,self.meta,self.root/'second-recovery')
        self.assertEqual(self.client.mutations,[])

    def test_same_version_wrong_channel_tag_is_refused(self):
        self.run_publish(); self.client.mutations=[]; self.client.tag=self.meta['base_sha']
        with self.assertRaisesRegex(ValueError,'inconsistent channel tag'):
            m.publish(self.client,self.packages,self.meta,self.root/'second-recovery')
        self.assertEqual(self.client.mutations,[])

    def test_manifest_path_escape_is_refused_before_reading(self):
        path=self.packages/'maintenance-manifest.json'; manifest=json.loads(path.read_text())
        manifest['packages']['../outside']={}
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError,'safe package names'): self.run_publish()
        self.assertEqual(self.client.mutations,[])

    def test_corrupt_existing_version_checksum_is_refused(self):
        self.client.assets['version.txt']['data']=b'v1.2.2-hfgj.333333333333\n'
        with self.assertRaisesRegex(ValueError,'version/checksum'): self.run_publish()
        self.assertEqual(self.client.mutations,[])

    def test_stable_version_downgrade_is_refused(self):
        data=b'v9.0.0-hfgj.222222222222\n'; self.client.assets['version.txt']['data']=data
        self.client.assets['SHA256SUMS']['data']=(m.digest(data)+'  version.txt\n').encode()
        with self.assertRaisesRegex(ValueError,'downgrade'): self.run_publish()
        self.assertEqual(self.client.mutations,[])

    def test_missing_target_is_refused(self):
        clean=self.root/'incomplete'; clean.mkdir()
        for path in self.packages.glob('*.gz'): (clean/path.name).write_bytes(path.read_bytes())
        with self.assertRaisesRegex(ValueError,'all five'): m.assemble(clean,self.meta)

    def test_wrong_executable_machine_is_refused(self):
        binary=bytearray(128); binary[:4]=b'\xcf\xfa\xed\xfe'
        with self.assertRaisesRegex(ValueError,'macOS'): m.check_binary(binary,'macho',0x100000c)

    def test_linux_dynamic_or_interpreter_segment_is_refused(self):
        path=next(self.packages.glob('mihomo-linux*.gz'))
        binary=bytearray(m.unpack(path,'mihomo-linux-arm64','gz')); binary[64:68]=(3).to_bytes(4,'little')
        with self.assertRaisesRegex(ValueError,'not static'): m.check_binary(binary,'elf',183)

    def test_duplicate_existing_checksums_are_refused(self):
        with self.assertRaisesRegex(ValueError,'checksums'): m.checksum_records((('a'*64+'  file\n')*2).encode())

    def test_alpha_metadata_is_refused(self):
        self.meta['channel']='HFGJ-Alpha'
        with self.assertRaises(ValueError): self.run_publish()


class DownloadTests(unittest.TestCase):
    def setUp(self):
        self.asset={'name':'SHA256SUMS','digest':'sha256:'+m.digest(b'expected')}

    def test_transient_http_and_stale_bytes_retry_then_succeed(self):
        from unittest.mock import Mock
        client=Mock(); client.download.side_effect=[
            urllib.error.HTTPError('public',404,'not yet visible',None,None),
            urllib.error.HTTPError('public',503,'unavailable',None,None),b'stale',b'expected']
        with patch.object(m.time,'sleep') as sleep:
            m.verify_download(client,self.asset,b'expected')
        self.assertEqual(client.download.call_count,4)
        self.assertEqual([call.args[0] for call in sleep.call_args_list],[1,2,4])
        keys=[call.kwargs['cache_key'] for call in client.download.call_args_list]
        self.assertEqual(len(set(keys)),4)

    def test_permanent_http_error_is_not_retried(self):
        from unittest.mock import Mock
        client=Mock(); client.download.side_effect=urllib.error.HTTPError('public',403,'forbidden',None,None)
        with patch.object(m.time,'sleep') as sleep:
            with self.assertRaises(urllib.error.HTTPError):m.verify_download(client,self.asset,b'expected')
        self.assertEqual(client.download.call_count,1);sleep.assert_not_called()

    def test_api_digest_conflict_is_not_retried_or_downloaded(self):
        from unittest.mock import Mock
        client=Mock();self.asset['digest']='sha256:'+'0'*64
        with self.assertRaisesRegex(ValueError,'API digest differs'):m.verify_download(client,self.asset,b'expected')
        client.download.assert_not_called()

    def test_network_errors_have_bounded_retries_and_safe_diagnostics(self):
        from unittest.mock import Mock
        client=Mock();client.download.side_effect=urllib.error.URLError('sensitive remote reason')
        with patch.object(m.time,'sleep') as sleep:
            with self.assertRaisesRegex(ValueError,'five attempts') as error:m.verify_download(client,self.asset,b'expected')
        self.assertEqual(client.download.call_count,5)
        self.assertEqual(sum(call.args[0] for call in sleep.call_args_list),15)
        diagnostic=m.safe_error(error.exception)
        self.assertEqual(diagnostic['cause']['type'],'URLError')
        self.assertNotIn('sensitive remote reason',json.dumps(diagnostic))

    def test_download_cache_key_uses_public_url_without_credentials(self):
        client=m.GitHub('test-private-token')
        asset={'browser_download_url':'https://github.com/hfgj/mihomo/releases/download/HFGJ-Stable/version.txt'}
        key=m.digest(b'expected')+'-1'
        with patch.object(m.urllib.request,'urlopen',return_value=io.BytesIO(b'expected')) as opened:
            self.assertEqual(client.download(asset,cache_key=key),b'expected')
        request=opened.call_args.args[0]
        self.assertTrue(request.full_url.endswith('?hfgj_verify='+key))
        self.assertIsNone(request.get_header('Authorization'))
        self.assertNotIn('test-private-token',request.full_url)

    def test_invalid_cache_key_refused_before_request(self):
        client=m.GitHub('test-private-token')
        asset={'browser_download_url':'https://github.com/hfgj/mihomo/releases/download/HFGJ-Stable/version.txt'}
        with patch.object(m.urllib.request,'urlopen') as opened:
            with self.assertRaisesRegex(ValueError,'cache key'):client.download(asset,cache_key='unexpected&other=value')
        opened.assert_not_called()


class GitTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(); self.root=Path(self.temp.name)
        self.seed=self.root/'seed'; self.seed.mkdir()
        self.call(self.seed,'init','-b','Meta')
        self.call(self.seed,'config','user.name','fixture')
        self.call(self.seed,'config','user.email','fixture@example.invalid')
        self.commit(self.seed,'base.txt','base')
        self.base=self.call(self.seed,'rev-parse','HEAD'); self.call(self.seed,'tag','v1.0.0')
        self.upstream=self.root/'upstream.git'; self.origin=self.root/'origin.git'
        self.call(self.root,'clone','--bare',str(self.seed),str(self.upstream))
        self.call(self.seed,'checkout','-b','hfgj'); self.commit(self.seed,'patch.txt','patch')
        self.patch=self.call(self.seed,'rev-parse','HEAD')
        self.call(self.root,'clone','--bare',str(self.seed),str(self.origin))
        self.work=self.clone('work')

    def tearDown(self): self.temp.cleanup()

    def call(self, root, *args):
        return subprocess.check_output(['git',*args],cwd=root,text=True,stderr=subprocess.PIPE).strip()

    def commit(self, root, name, value):
        (root/name).write_text(value)
        self.call(root,'add',name); self.call(root,'commit','-m','fixture '+name)

    def clone(self,name):
        path=self.root/name
        self.call(self.root,'clone','--branch','hfgj',str(self.origin),str(path))
        return path

    def advance(self, conflict=False):
        source=self.root/'advance'; self.call(self.root,'clone',str(self.upstream),str(source))
        self.call(source,'config','user.name','fixture'); self.call(source,'config','user.email','fixture@example.invalid')
        self.commit(source,'base.txt' if conflict else 'next.txt','upstream change')
        self.call(source,'push','origin','Meta')
        return self.call(source,'rev-parse','HEAD')

    def refs(self):
        return {name:self.call(self.origin,'rev-parse',name) for name in ('Meta','hfgj')}

    def prepare(self,published=None):
        return m.prepare(self.work,self.root/'locked',str(self.upstream),published)

    def test_no_change_skips_build_and_push(self):
        meta=self.prepare('v1.0.0-hfgj.'+self.patch[:12])
        self.assertFalse(meta['changed']); self.assertEqual(self.refs(),{'Meta':self.base,'hfgj':self.patch})

    def test_merge_preview_does_not_change_remote_then_atomic_promotion(self):
        upstream=self.advance(); meta=self.prepare()
        self.assertEqual(self.refs(),{'Meta':self.base,'hfgj':self.patch})
        self.assertEqual(meta['upstream_sha'],upstream); self.assertFalse(meta['upstream_is_tag'])
        promoter=self.clone('promoter'); m.promote(promoter,self.root/'locked',meta)
        self.assertEqual(self.refs(),{'Meta':upstream,'hfgj':meta['source_sha']})
        self.assertTrue(m.ancestor(promoter,self.patch,meta['source_sha']))

    def test_same_parents_make_same_candidate_commit(self):
        self.advance(); meta=self.prepare()
        other=self.clone('other')
        second=m.prepare(other,self.root/'locked2',str(self.upstream))
        self.assertEqual(second['source_sha'],meta['source_sha'])

    def test_conflict_retains_both_refs_and_aborts_merge(self):
        self.call(self.work,'config','user.name','fixture'); self.call(self.work,'config','user.email','fixture@example.invalid')
        self.commit(self.work,'base.txt','patch change'); self.call(self.work,'push','origin','hfgj')
        before=self.refs(); self.advance(True)
        with self.assertRaisesRegex(ValueError,'merge conflict'): self.prepare()
        self.assertEqual(self.refs(),before); self.assertEqual(self.call(self.work,'status','--porcelain'),'')
        self.assertFalse((self.root/'locked').exists())

    def test_diverged_mirror_is_refused(self):
        self.call(self.origin,'update-ref','refs/heads/Meta',self.patch)
        self.advance()
        with self.assertRaisesRegex(ValueError,'diverged'): self.prepare()
        self.assertEqual(self.refs(),{'Meta':self.patch,'hfgj':self.patch})

    def test_concurrent_patch_update_is_not_overwritten(self):
        self.advance(); meta=self.prepare()
        self.commit(self.seed,'race.txt','human push'); self.call(self.seed,'push',str(self.origin),'hfgj')
        before=self.refs(); promoter=self.clone('promoter')
        with self.assertRaisesRegex(ValueError,'Concurrent'): m.promote(promoter,self.root/'locked',meta)
        self.assertEqual(self.refs(),before)

    def test_atomic_server_rejection_keeps_both_refs(self):
        self.advance(); meta=self.prepare(); before=self.refs()
        hook=self.origin/'hooks/pre-receive'; hook.write_text('#!/bin/sh\nexit 1\n'); hook.chmod(0o755)
        promoter=self.clone('promoter')
        with self.assertRaises(subprocess.CalledProcessError): m.promote(promoter,self.root/'locked',meta)
        self.assertEqual(self.refs(),before)

    def test_dirty_source_is_refused(self):
        (self.work/'private-placeholder').write_text('fixture')
        with self.assertRaisesRegex(ValueError,'Uncommitted'): self.prepare()

    def test_tampered_bundle_identity_is_refused(self):
        self.advance(); meta=self.prepare(); meta['source_sha']='8'*40
        meta['version']='v1.0.0-hfgj.'+'8'*12
        promoter=self.clone('promoter'); before=self.refs()
        with self.assertRaisesRegex(ValueError,'bundle identity'): m.promote(promoter,self.root/'locked',meta)
        self.assertEqual(self.refs(),before)


if __name__=='__main__': unittest.main()

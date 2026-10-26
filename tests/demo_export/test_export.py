import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('demo_export', ROOT / 'scripts/demo-export.py')
export = importlib.util.module_from_spec(spec)
spec.loader.exec_module(export)


def webp_available():
    if not shutil.which('ffmpeg') or not shutil.which('ffprobe'):
        return False
    return bool(shutil.which('cwebp')) or 'libwebp' in export.run(['ffmpeg', '-hide_banner', '-encoders']).stdout


class ExportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.media = dict(width=1398, height=2034, duration=20)
        self.cuts = dict(coverIn=0, coverOut=8, openIn=9, openOut=20,
                         crop=dict(x=0, y=525, width=1398, height=984))
        self.paths = {}
        for theme in ('light', 'dark'):
            directory = self.root / theme
            directory.mkdir()
            (directory / 'video.mp4').write_bytes(b'video')
            (directory / 'unfolded-preview.png').write_bytes(b'\x89PNG\r\n\x1a\n')
            data = dict(status='complete', appearance=theme, profile={'id': 'iphone-duo'},
                        run_id=theme + '-run', app={'executable_sha256': 'a' * 64},
                        source_commit='b' * 40, source_dirty=False,
                        runner_script_sha256='c' * 64, ui_map_sha256='d' * 64,
                        artifacts=[{'file': 'video.mp4', 'type': 'website-video', 'appearance': theme},
                                   {'file': 'unfolded-preview.png', 'type': 'screenshot', 'pose': 'unfolded'}])
            self.paths[theme] = directory / 'manifest.json'
            self.paths[theme].write_text(json.dumps(data))
            self.refresh_artifact_hashes(theme)
        self.cut_path = self.root / 'cuts.json'
        self.cut_path.write_text(json.dumps({theme: self.cuts for theme in self.paths}))
        self.output = self.root / 'staging'

    def args(self):
        return ['--light-manifest', str(self.paths['light']), '--dark-manifest', str(self.paths['dark']),
                '--cuts', str(self.cut_path), '--output', str(self.output)]

    def mutate(self, theme, change):
        data = export.read_json(self.paths[theme])
        change(data)
        self.paths[theme].write_text(json.dumps(data))

    def refresh_artifact_hashes(self, theme):
        def refresh(data):
            for artifact in data['artifacts']:
                path = self.paths[theme].parent / artifact['file']
                artifact['sha256'] = hashlib.sha256(path.read_bytes()).hexdigest()
        self.mutate(theme, refresh)

    def test_uppercase_artifact_hashes_accepted(self):
        def uppercase(data):
            for artifact in data['artifacts']:
                artifact['sha256'] = artifact['sha256'].upper()
        self.mutate('light', uppercase)
        with patch.object(export, 'probe', return_value=self.media) as probe:
            export.load_capture(self.paths['light'], 'light', self.cuts)
        self.assertEqual(probe.call_count, 2)

    def test_missing_and_invalid_artifact_hashes_rejected_before_probe(self):
        original = self.paths['light'].read_text()
        cases = [{}] + [{'sha256': value} for value in (
            None, '', 'a' * 63, 'a' * 65, 'g' * 64, 'a' * 64 + '\n',
            1, False, [], {})]
        for index in (0, 1):
            for fields in cases:
                with self.subTest(artifact=index, fields=fields):
                    self.paths['light'].write_text(original)
                    def change(data):
                        artifact = data['artifacts'][index]
                        artifact.pop('sha256')
                        artifact.update(fields)
                    self.mutate('light', change)
                    with patch.object(export, 'probe') as probe:
                        with self.assertRaisesRegex(export.ExportError, 'valid SHA256'):
                            export.load_capture(self.paths['light'], 'light', self.cuts)
                    probe.assert_not_called()

    def test_mismatched_artifact_hashes_rejected_before_probe(self):
        original = self.paths['light'].read_text()
        for index in (0, 1):
            with self.subTest(artifact=index):
                self.paths['light'].write_text(original)
                self.mutate('light', lambda data: data['artifacts'][index].update(sha256='0' * 64))
                with patch.object(export, 'probe') as probe:
                    with self.assertRaisesRegex(export.ExportError, 'SHA256 mismatch'):
                        export.load_capture(self.paths['light'], 'light', self.cuts)
                probe.assert_not_called()

    def test_changed_artifact_bytes_rejected_before_staging_or_encoding(self):
        for theme in ('light', 'dark'):
            for name in ('video.mp4', 'unfolded-preview.png'):
                with self.subTest(theme=theme, file=name):
                    path = self.paths[theme].parent / name
                    original = path.read_bytes()
                    path.write_bytes(original + b'tampered')
                    try:
                        with patch.object(export, 'probe', return_value=self.media) as probe, \
                                patch.object(export, 'check_encoders') as encoders, \
                                patch.object(export, 'ffmpeg') as ffmpeg, \
                                contextlib.redirect_stderr(io.StringIO()) as error:
                            self.assertEqual(export.main(self.args()), 1)
                        self.assertIn('SHA256 mismatch: ' + name, error.getvalue())
                        self.assertEqual(probe.call_count, 0 if theme == 'light' else 2)
                        encoders.assert_not_called()
                        ffmpeg.assert_not_called()
                        self.assertFalse(self.output.exists())
                    finally:
                        path.write_bytes(original)

    def test_complete_capture_uses_actual_probe(self):
        with patch.object(export, 'probe', return_value=self.media) as probe:
            capture = export.load_capture(self.paths['light'], 'light', self.cuts)
        self.assertEqual(probe.call_count, 2)
        self.assertEqual(capture['video'].name, 'video.mp4')

    def test_failed_manifest_rejected_before_probe_or_staging(self):
        self.mutate('dark', lambda data: data.update(status='failed'))
        with patch.object(export, 'probe', return_value=self.media), patch.object(export, 'ffmpeg') as ffmpeg:
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(export.main(self.args()), 1)
        ffmpeg.assert_not_called()
        self.assertFalse(self.output.exists())

    def test_wrong_theme_profile_and_missing_provenance_rejected(self):
        original = self.paths['light'].read_text()
        for changes in ({'appearance': 'dark'}, {'profile': {'id': 'iphone-17-pro'}},
                        {'ui_map_sha256': None}, {'source_dirty': 'false'}, {'run_id': ''}):
            with self.subTest(changes=changes):
                self.paths['light'].write_text(original)
                self.mutate('light', lambda data: data.update(changes))
                with self.assertRaises(export.ExportError), patch.object(export, 'probe') as probe:
                    export.load_capture(self.paths['light'], 'light', self.cuts)
                probe.assert_not_called()

    def test_missing_video_preview_and_escaped_artifact_rejected(self):
        for name in ('video.mp4', 'unfolded-preview.png'):
            path = self.paths['light'].parent / name
            content = path.read_bytes()
            path.unlink()
            with self.subTest(name=name), self.assertRaises(export.ExportError):
                export.load_capture(self.paths['light'], 'light', self.cuts)
            path.write_bytes(content)
        outside = self.root / 'outside.mp4'
        outside.touch()
        self.mutate('light', lambda data: data['artifacts'][0].update(file='../outside.mp4'))
        with self.assertRaises(export.ExportError):
            export.load_capture(self.paths['light'], 'light', self.cuts)

    def test_invalid_order_nonfinite_and_out_of_bounds_cuts(self):
        for changes in ({'coverIn': -1}, {'coverOut': 0}, {'openIn': 7}, {'openOut': 21},
                        {'openOut': float('nan')}, {'openIn': True}, {'coverIn': '0'}):
            with self.subTest(changes=changes), self.assertRaises(export.ExportError):
                export.validate_cuts(dict(self.cuts, **changes), self.media)
        export.validate_cuts(self.cuts, self.media)

    def test_crop_must_fit_actual_frame(self):
        for crop in (None, {}, dict(x=-1, y=0, width=10, height=10),
                     dict(x=0, y=2030, width=10, height=10),
                     dict(x=1390, y=0, width=10, height=10),
                     dict(x=0, y=0, width=0, height=10),
                     dict(x=False, y=0, width=10, height=10),
                     dict(x=0, y=0, width=10.5, height=10)):
            with self.subTest(crop=crop), self.assertRaises(export.ExportError):
                export.validate_cuts(dict(self.cuts, crop=crop), self.media)

    def test_existing_output_never_runs_ffmpeg_or_clobbers(self):
        self.output.mkdir()
        marker = self.output / 'keep'
        marker.write_text('preserve')
        with patch.object(export, 'run') as run, contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(export.main(self.args()), 1)
        run.assert_not_called()
        self.assertEqual(marker.read_text(), 'preserve')

    def test_source_paths_relative_only_inside_repo(self):
        self.assertEqual(export.source_path(ROOT / 'scripts/demo-export.py'), 'scripts/demo-export.py')
        self.assertEqual(export.source_path(self.root), str(self.root.resolve()))

    def test_missing_webp_encoder_fails_before_staging(self):
        with patch.object(export, 'probe', return_value=self.media), \
                patch.object(export, 'run') as run, patch.object(export.shutil, 'which', return_value=None), \
                contextlib.redirect_stderr(io.StringIO()):
            run.return_value.stdout = ' V....D libx264 H264 encoder'
            self.assertEqual(export.main(self.args()), 1)
        self.assertFalse(self.output.exists())

    def test_cwebp_fallback_cleans_private_png_on_success_and_failure(self):
        self.output.mkdir()
        destination = self.output / 'poster.webp'
        for fail in (False, True):
            with self.subTest(fail=fail):
                temporary_paths = []
                def decode(png, *args):
                    self.assertEqual(png.parent.parent, self.output)
                    self.assertEqual(png.parent.stat().st_mode & 0o777, 0o700)
                    temporary_paths.append(png)
                    png.write_bytes(b'PNG test stub')
                def encode(argv):
                    self.assertEqual(argv[:3], ['cwebp', '-lossless', '-quiet'])
                    self.assertEqual(argv[-2:], ['-o', destination])
                    self.assertTrue(Path(argv[3]).is_file())
                    if fail:
                        raise OSError('test encoder failure')
                with patch.object(export, 'ffmpeg', side_effect=decode), patch.object(export, 'run', side_effect=encode):
                    if fail:
                        with self.assertRaises(OSError):
                            export.lossless_webp(destination, '-i', 'input.mp4', backend='cwebp')
                    else:
                        export.lossless_webp(destination, '-i', 'input.mp4', backend='cwebp')
                self.assertEqual(len(temporary_paths), 1)
                self.assertFalse(temporary_paths[0].parent.exists())

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'ffmpeg/ffprobe required')
    def test_real_h264_pipeline_and_exact_odd_crop_command(self):
        capture = dict(manifest=export.read_json(self.paths['light']), path=self.paths['light'],
                       video=self.paths['light'].parent / 'video.mp4',
                       preview=self.paths['light'].parent / 'unfolded-preview.png', media=self.media,
                       cuts=dict(coverIn=0, coverOut=.4, openIn=.5, openOut=1,
                                 crop=dict(x=0, y=25, width=64, height=45)))
        export.run(['ffmpeg', '-nostdin', '-v', 'error', '-y', '-f', 'lavfi', '-i',
                    'color=c=blue:s=64x96:r=30:d=1', '-c:v', 'libx264', '-pix_fmt', 'yuv420p',
                    capture['video']])
        self.output.mkdir()
        real_ffmpeg = export.ffmpeg
        commands = []
        def encode(path, *args):
            commands.append(args)
            if path.suffix == '.webp':
                path.write_bytes(b'poster test stub')
            else:
                real_ffmpeg(path, *args)
        real_probe = export.probe
        def probe(path):
            if path.suffix == '.webp':
                return dict(width=1280, height=900)
            return real_probe(path)
        with patch.object(export, 'ffmpeg', side_effect=encode), patch.object(export, 'probe', side_effect=probe):
            result = export.export_theme('light', capture, self.output)
        open_command = next(args for args in commands if 'crop=64:45:0:25:exact=1' in ','.join(map(str, args)))
        self.assertIn('crop=64:45:0:25:exact=1,scale=1280:900:flags=lanczos,setsar=1,fps=30', open_command)
        for pose, dims in (('cover', (742, 1080)), ('open', (1280, 900))):
            media = real_probe(self.output / result[pose]['file'])
            self.assertEqual((media['width'], media['height']), dims)
            self.assertEqual(media['codec_name'], 'h264')
            self.assertEqual(media['pix_fmt'], 'yuv420p')
            self.assertFalse(media['audio'])
        self.assertEqual(result['sourceDirty'], False)
        self.assertEqual(result['mapHash'], 'd' * 64)

    @unittest.skipUnless(webp_available(), 'ffmpeg with libwebp or cwebp required for full export')
    def test_synthetic_export_pipeline_and_metadata(self):
        # Synthetic test fixture only; no app, simulator or real recordings involved.
        for theme, path in self.paths.items():
            export.run(['ffmpeg', '-nostdin', '-v', 'error', '-y', '-f', 'lavfi', '-i',
                        'color=c=blue:s=64x96:r=30:d=1', '-c:v', 'libx264', '-pix_fmt', 'yuv420p',
                        path.parent / 'video.mp4'])
            export.run(['ffmpeg', '-nostdin', '-v', 'error', '-y', '-i', path.parent / 'video.mp4',
                        '-frames:v', '1', path.parent / 'unfolded-preview.png'])
            self.refresh_artifact_hashes(theme)
        cuts = dict(coverIn=0, coverOut=.4, openIn=.5, openOut=1,
                    crop=dict(x=0, y=24, width=64, height=45))
        self.cut_path.write_text(json.dumps({theme: cuts for theme in self.paths}))
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(export.main(self.args()), 0)
        metadata = export.read_json(self.output / 'export.json')
        self.assertFalse(metadata['visualQualityApproved'])
        self.assertEqual(len(list(self.output.iterdir())), 11)
        for theme in self.paths:
            result = metadata['duo'][theme]
            self.assertEqual(result['runId'], theme + '-run')
            self.assertEqual(result['appHash'], 'a' * 64)
            self.assertEqual(result['cuts'], cuts)
            for pose, dimensions in (('cover', (742, 1080)), ('open', (1280, 900))):
                item = result[pose]
                self.assertEqual((item['width'], item['height']), dimensions)
                self.assertGreater(item['bytes'], 0)
                self.assertEqual(item['poster']['width'], item['width'])
                self.assertAlmostEqual(item['duration'], cuts[pose + 'Out'] - cuts[pose + 'In'], delta=.1)
            self.assertEqual(result['preview']['width'], 1280)

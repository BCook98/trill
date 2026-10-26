#!/usr/bin/env python3
"""Export completed Duo captures into a new staging directory.

Cuts JSON: {"light": {"coverIn": 0, "coverOut": 10, "openIn": 11,
"openOut": 20, "crop": {"x": 0, "y": 500, "width": 1398, "height": 984}},
"dark": {same keys}}. Crop applies to the open clip; cover uses the full frame.
Requires ffmpeg/ffprobe with libx264 and either libwebp or standalone cwebp.
This tool validates media geometry, not visual quality or privacy.
"""
import argparse
import hashlib
import json
import math
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]


class ExportError(ValueError):
    pass


def read_json(path):
    with Path(path).open() as stream:
        return json.load(stream)


def run(argv):
    return subprocess.run([str(x) for x in argv], check=True, capture_output=True, text=True)


def probe(path):
    info = json.loads(run(['ffprobe', '-v', 'error', '-show_streams', '-show_format',
                           '-of', 'json', path]).stdout)
    videos = [s for s in info['streams'] if s.get('codec_type') == 'video']
    if len(videos) != 1:
        raise ExportError('Expected exactly one video/image stream')
    stream = videos[0]
    if stream.get('width', 0) <= 0 or stream.get('height', 0) <= 0:
        raise ExportError('Invalid media dimensions')
    return dict(stream, duration=float(info.get('format', {}).get('duration', 0)),
                audio=any(s.get('codec_type') == 'audio' for s in info['streams']))


def source_path(path):
    path = Path(path).resolve()
    return str(path.relative_to(ROOT)) if path.is_relative_to(ROOT) else str(path)


def artifact_path(manifest_path, artifact):
    name = artifact.get('file')
    if not isinstance(name, str) or not name:
        raise ExportError('Artifact needs a file path')
    base = manifest_path.parent.resolve()
    path = (base / name).resolve()
    if not path.is_relative_to(base) or not path.is_file():
        raise ExportError('Artifact must exist inside its capture directory')
    return path


def verify_artifact_hash(path, artifact):
    expected = artifact.get('sha256')
    if not isinstance(expected, str) or not re.fullmatch('[0-9a-fA-F]{64}', expected):
        raise ExportError('Artifact needs a valid SHA256: ' + path.name)
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    if digest.hexdigest() != expected.lower():
        raise ExportError('Artifact SHA256 mismatch: ' + path.name)


def number(value):
    return type(value) in (int, float) and math.isfinite(value)


def validate_cuts(cuts, media):
    if not isinstance(cuts, dict):
        raise ExportError('Cuts must be an object for each appearance')
    times = [cuts.get(key) for key in ('coverIn', 'coverOut', 'openIn', 'openOut')]
    if (not all(number(v) for v in times) or not number(media['duration'])
            or not 0 <= times[0] < times[1] <= times[2] < times[3] <= media['duration']):
        raise ExportError('Cuts must be ordered, nonempty and within actual video duration')
    crop = cuts.get('crop')
    if not isinstance(crop, dict) or set(crop) != {'x', 'y', 'width', 'height'}:
        raise ExportError('Open crop requires x/y/width/height')
    if (any(type(v) is not int for v in crop.values()) or crop['x'] < 0 or crop['y'] < 0
            or crop['width'] <= 0 or crop['height'] <= 0
            or crop['x'] + crop['width'] > media['width']
            or crop['y'] + crop['height'] > media['height']):
        raise ExportError('Open crop exceeds actual video bounds')


def load_capture(path, theme, cuts):
    path = Path(path).resolve()
    data = read_json(path)
    if (not isinstance(data, dict) or data.get('status') != 'complete'
            or data.get('appearance') != theme or data.get('profile', {}).get('id') != 'iphone-duo'):
        raise ExportError('Expected a complete Duo manifest for ' + theme)
    if not isinstance(data.get('run_id'), str) or not data['run_id']:
        raise ExportError('Manifest needs a run ID')
    for value in (data.get('app', {}).get('executable_sha256'),
                  data.get('runner_script_sha256'), data.get('ui_map_sha256')):
        if not isinstance(value, str) or not re.fullmatch('[0-9a-fA-F]{64}', value):
            raise ExportError('Manifest needs app, runner and map SHA256 provenance')
    if not isinstance(data.get('source_commit'), str) or type(data.get('source_dirty')) is not bool:
        raise ExportError('Manifest needs source commit and dirty-state provenance')
    artifacts = data.get('artifacts', [])
    videos = [a for a in artifacts if a.get('type') == 'website-video' and a.get('appearance') == theme]
    previews = [a for a in artifacts if a.get('type') == 'screenshot'
                and a.get('pose') == 'unfolded' and str(a.get('file', '')).endswith('-preview.png')]
    if len(videos) != 1 or len(previews) != 1:
        raise ExportError('Manifest needs exactly one website video and unfolded preview PNG')
    video, preview = artifact_path(path, videos[0]), artifact_path(path, previews[0])
    verify_artifact_hash(video, videos[0])
    verify_artifact_hash(preview, previews[0])
    with preview.open('rb') as stream:
        if stream.read(8) != b'\x89PNG\r\n\x1a\n':
            raise ExportError('Preview is not a PNG')
    media = probe(video)
    validate_cuts(cuts, media)
    preview_media = probe(preview)
    return dict(manifest=data, path=path, video=video, preview=preview,
                media=media, preview_media=preview_media, cuts=cuts)


def check_encoders():
    encoders = run(['ffmpeg', '-hide_banner', '-encoders']).stdout
    names = {line.split()[1] for line in encoders.splitlines() if len(line.split()) >= 2}
    if 'libx264' not in names:
        raise ExportError('ffmpeg requires libx264; staging was not created')
    if 'libwebp' in names:
        return 'libwebp'
    if shutil.which('cwebp'):
        return 'cwebp'
    raise ExportError('Lossless WebP requires ffmpeg libwebp or cwebp; staging was not created')


def ffmpeg(output, *args):
    # All callers supply fixed basenames in the exclusively created staging dir.
    run(['ffmpeg', '-nostdin', '-v', 'error', '-y', *args, output])


def lossless_webp(output, *input_args, backend='libwebp'):
    if backend == 'libwebp':
        ffmpeg(output, *input_args, '-frames:v', '1', '-c:v', 'libwebp', '-lossless', '1')
    elif backend == 'cwebp':
        # TemporaryDirectory is private (0700), under the new staging directory,
        # and cleans up the decoded/scaled PNG even when encoding fails.
        with tempfile.TemporaryDirectory(prefix='.webp-', dir=output.parent) as temporary:
            png = Path(temporary) / 'frame.png'
            ffmpeg(png, *input_args, '-frames:v', '1', '-pix_fmt', 'rgb24')
            run(['cwebp', '-lossless', '-quiet', png, '-o', output])
    else:
        raise ExportError('Unsupported WebP backend')


def file_metadata(path, media=None):
    media = probe(path) if media is None else media
    return dict(file=path.name, width=media['width'], height=media['height'], bytes=path.stat().st_size)


def export_theme(theme, capture, output, webp_backend='libwebp'):
    data, cuts = capture['manifest'], capture['cuts']
    result = dict(runId=data['run_id'], appHash=data['app']['executable_sha256'],
                  sourceCommit=data['source_commit'], sourceDirty=data['source_dirty'],
                  runnerHash=data['runner_script_sha256'], mapHash=data['ui_map_sha256'],
                  manifest=source_path(capture['path']), video=source_path(capture['video']),
                  previewSource=source_path(capture['preview']), cuts=cuts,
                  sourceMedia={key: capture['media'][key] for key in ('width', 'height', 'duration')},
                  timebase='MP4 presentation seconds')
    for pose, width, height in (('cover', 742, 1080), ('open', 1280, 900)):
        start, end = cuts[pose + 'In'], cuts[pose + 'Out']
        crop = cuts['crop'] if pose == 'open' else None
        filters = []
        if crop:
            filters.append('crop={width}:{height}:{x}:{y}:exact=1'.format(**crop))
        filters += [f'scale={width}:{height}:flags=lanczos', 'setsar=1', 'fps=30']
        video = output / f'duo-{theme}-{pose}.mp4'
        ffmpeg(video, '-i', capture['video'], '-ss', str(start), '-t', str(end - start),
               '-map', '0:v:0', '-vf', ','.join(filters), '-an', '-c:v', 'libx264',
               '-pix_fmt', 'yuv420p', '-crf', '23', '-preset', 'slow', '-movflags', '+faststart')
        media = probe(video)
        if (media['width'] != width or media['height'] != height or media['audio']
                or media.get('codec_name') != 'h264' or media.get('pix_fmt') != 'yuv420p'
                or media.get('avg_frame_rate') != '30/1'
                or not number(media['duration']) or media['duration'] <= 0
                or abs(media['duration'] - (end - start)) > 0.1):
            raise ExportError('Exported video failed validation')
        poster = video.with_suffix('.webp')
        lossless_webp(poster, '-i', video, backend=webp_backend)
        result[pose] = dict(file_metadata(video, media), duration=media['duration'],
                            crop=crop, poster=file_metadata(poster))
    preview = output / f'duo-{theme}-preview.webp'
    lossless_webp(preview, '-i', capture['preview'], '-vf', 'scale=1280:-1:flags=lanczos',
                  backend=webp_backend)
    result['preview'] = file_metadata(preview)
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--light-manifest', type=Path, required=True)
    parser.add_argument('--dark-manifest', type=Path, required=True)
    parser.add_argument('--cuts', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        output = args.output.absolute()
        if output.exists() or output.is_symlink():
            raise ExportError('Output directory must not already exist')
        cuts = read_json(args.cuts)
        if not isinstance(cuts, dict) or set(cuts) != {'light', 'dark'}:
            raise ExportError('Cuts JSON must contain light and dark objects')
        captures = {theme: load_capture(path, theme, cuts[theme]) for theme, path in
                    (('light', args.light_manifest), ('dark', args.dark_manifest))}
        webp_backend = check_encoders()
        output.mkdir(parents=True, exist_ok=False)
        metadata = dict(schema=1, visualQualityApproved=False, webpEncoder=webp_backend, duo={})
        for theme, capture in captures.items():
            metadata['duo'][theme] = export_theme(theme, capture, output, webp_backend)
        (output / 'export.json').write_text(json.dumps(metadata, indent=2) + '\n')
        print(output / 'export.json')
        return 0
    except (ExportError, OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as exc:
        # Keep partial exports for inspection, but never emit success metadata.
        print(f'Export failed: {exc}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())

# README

# TODO

[] check ALL printfs for proper formatting 

[] transcoding flac -> mp3  ; does this maintain the tags?

	ffmpeg.exe -i "bibio\Bibio - Fi - flac\Fi-002-Bibio-Bewley In White.flac" -codec:a libmp3lame -qscale:a 1 out-02.mp3

to analyze the format (so I know whether to convert or not); json format

	.\ffprobe.exe -v quiet -print_format json -show_format -show_streams -i ../../"bibio\Bibio - Fi - flac\Fi-002-Bibio-Bewley In White.flac"

	-print_format format  set the output printing format (available formats are: default, compact, csv, flat, ini, json, xml)

import sys
import os

pdf_path = sys.argv[1] if len(sys.argv) > 1 else 'Итоговый_отчет_по_практике_Парфенов_И_А.pdf'
out_path = sys.argv[2] if len(sys.argv) > 2 else 'report_text.txt'

try:
    from pypdf import PdfReader
except Exception:
    try:
        import subprocess
        subprocess.check_call([sys.executable, '-m', 'pip', 'install', 'pypdf'])
        from pypdf import PdfReader
    except Exception as e:
        print('Failed to install or import pypdf:', e)
        sys.exit(2)

try:
    reader = PdfReader(pdf_path)
    with open(out_path, 'w', encoding='utf-8') as f:
        for p in reader.pages:
            try:
                text = p.extract_text() or ''
            except Exception:
                text = ''
            f.write(text + '\n')
    print('WROTE', out_path)
except Exception as e:
    print('ERROR reading PDF:', e)
    sys.exit(3)

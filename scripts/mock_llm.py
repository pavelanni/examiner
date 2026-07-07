import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def _send(self, code, obj):
        data = json.dumps(obj).encode('utf-8')
        self.send_response(code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path.startswith('/v1/models'):
            obj = {"data":[{"id":"mock-model","object":"model"}], "object":"list"}
            self._send(200, obj)
        else:
            self._send(404, {"error":"not found"})

    def do_POST(self):
        if self.path.startswith('/v1/chat/completions'):
            length = int(self.headers.get('Content-Length', 0))
            body = self.rfile.read(length).decode('utf-8')
            # Return a single choice where message.content is a JSON string matching GradeResult
            grade = {"score":1.0, "max_points":10, "feedback":"Mock feedback","need_followup":False, "followup_question":""}
            message_content = json.dumps(grade)
            resp = {
                "id": "mock",
                "choices": [
                    {"message": {"role":"assistant","content": message_content}}
                ],
                "usage": {"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
            }
            self._send(200, resp)
        else:
            self._send(404, {"error":"not found"})

if __name__ == '__main__':
    port = 11434
    if len(sys.argv) > 1:
        try:
            port = int(sys.argv[1])
        except:
            pass
    server = HTTPServer(('0.0.0.0', port), Handler)
    print('Mock LLM listening on', port)
    server.serve_forever()

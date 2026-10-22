#!/usr/bin/env python3
import http.server
import urllib.request
import urllib.error
import urllib.parse
import sys
import json
import os

PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 3000

class KidsPainterProxyHandler(http.server.SimpleHTTPRequestHandler):
    def end_headers(self):
        # 为静态资源也注入开放 CORS 头
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'GET, POST, OPTIONS, PUT, DELETE')
        self.send_header('Access-Control-Allow-Headers', 'Authorization, Content-Type, x-target-url, x-api-key')
        super().end_headers()

    def do_OPTIONS(self):
        self.send_response(200)
        self.end_headers()

    def do_POST(self):
        # 匹配代理转发路由
        if self.path.startswith('/proxy'):
            target_url = self.headers.get('x-target-url')
            if not target_url:
                query = urllib.parse.urlparse(self.path).query
                params = urllib.parse.parse_qs(query)
                target_url = params.get('url', [None])[0]

            if not target_url:
                self.send_response(400)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(b'{"error": "missing target url query parameter or x-target-url header"}')
                return

            length = int(self.headers.get('Content-Length', 0))
            body = self.rfile.read(length) if length > 0 else None

            req = urllib.request.Request(target_url, data=body, method='POST')
            for h in ['Authorization', 'Content-Type', 'x-api-key']:
                if h in self.headers:
                    req.add_header(h, self.headers[h])

            try:
                with urllib.request.urlopen(req, timeout=180) as resp:
                    resp_data = resp.read()
                    self.send_response(resp.status)
                    for k, v in resp.headers.items():
                        if k.lower() in ['content-type']:
                            self.send_header(k, v)
                    self.end_headers()
                    self.wfile.write(resp_data)
            except urllib.error.HTTPError as e:
                err_data = e.read()
                self.send_response(e.code)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(err_data)
            except Exception as e:
                err_msg = str(e)
                print(f"[Proxy Error] Forwarding to {target_url} failed: {err_msg}", file=sys.stderr)
                self.send_response(502)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                self.wfile.write(json.dumps({'error': f'中转代理连接目标服务器失败 ({err_msg})，请检查网络或目标服务器状态'}).encode())
            return

        super().do_POST()

def main():
    os.chdir(os.path.dirname(os.path.abspath(__file__)))
    server_address = ('0.0.0.0', PORT)
    httpd = http.server.ThreadingHTTPServer(server_address, KidsPainterProxyHandler)
    print(f"Kids Painter server with proxy running on http://0.0.0.0:{PORT}")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass

if __name__ == '__main__':
    main()

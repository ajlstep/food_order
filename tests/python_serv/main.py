
from http.server import HTTPServer, BaseHTTPRequestHandler
from datetime import datetime


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        message = f"""
        <!DOCTYPE html>
        <html>
        <head>
            <meta charset="utf-8">
            <title>Python Test Server</title>
        </head>
        <body>
            <h1>Python server works!</h1>
            <p>Ngrok connection is working.</p>
            <p>Path: {self.path}</p>
            <p>Time: {datetime.now()}</p>
        </body>
        </html>
        """

        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        self.wfile.write(message.encode("utf-8"))

    def log_message(self, format, *args):
        print(f"[{datetime.now():%H:%M:%S}] {format % args}")


server = HTTPServer(("0.0.0.0", 8080), Handler)

print("Server running at http://localhost:8080")
server.serve_forever()

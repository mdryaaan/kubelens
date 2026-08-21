/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,

  // The dashboard talks to the Go server. In development the two run on
  // different ports, so requests are proxied through Next rather than made
  // cross-origin — that keeps the browser's CORS rules out of the picture for
  // everything except the SSE stream, which needs a direct connection anyway.
  async rewrites() {
    const target = process.env.KUBELENS_API ?? 'http://127.0.0.1:8080';
    return [{ source: '/api/:path*', destination: `${target}/api/:path*` }];
  },
};

export default nextConfig;

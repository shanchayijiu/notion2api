import type { NextConfig } from 'next';

const nextConfig: NextConfig = {
  output: 'export',
  basePath: '/admin',
  assetPrefix: '/admin/',
  images: {
    unoptimized: true,
  },
};

export default nextConfig;

import express, { Express } from 'express';
import socket from 'socket.io';
import bodyParser from 'body-parser';
import cookieParser from 'cookie-parser';
import http from 'http';
import { api } from './api';
import dotenv from 'dotenv';
import { handleConnection } from './services/matchmaking';
import { logger, requestLogger } from './utils';

dotenv.config({ path: 'prisma/.env' });

const app = express();

app.use(requestLogger);
app.use(bodyParser.json());
app.use(cookieParser(''));

app.use('/api', api);

const server: http.Server = http.createServer(app);

server.listen(3000, () => {
  logger.info(`Listening on port 3000.`);
})

const io: socket.Server = new socket.Server(server);

handleConnection(io);

#include <iostream>
#include <string>
#include <vector>
#include <map>
#include <unordered_map>
#include <deque>
#include <algorithm>
#include <sstream>
#include <iomanip>
#include <uWebSockets/App.h>

using namespace std;

long long parsePrice(string_view s) {
    long long res = 0;
    int dot = -1;
    long long frac = 0;
    int frac_len = 0;
    for (size_t i = 0; i < s.length(); ++i) {
        if (s[i] == '.') {
            dot = i;
        } else if (dot == -1) {
            res = res * 10 + (s[i] - '0');
        } else {
            if (frac_len < 4) {
                frac = frac * 10 + (s[i] - '0');
                frac_len++;
            }
        }
    }
    while (frac_len < 4) {
        frac *= 10;
        frac_len++;
    }
    return res * 10000 + frac;
}

struct Order {
    string id;
    char side;
    char type;
    long long price;
    long long qty;
    long long filled;
    long long seq;
    long long remaining() const { return qty - filled; }
};

struct Fill {
    string maker, taker;
    long long price, qty;
};

struct Book {
    map<long long, vector<Order*>, greater<long long>> bids;
    map<long long, vector<Order*>, less<long long>> asks;
    unordered_map<string, Order*> orders;
    deque<Order> pool;
    long long seq = 0;

    vector<Fill> match(Order* taker) {
        vector<Fill> fills;
        if (taker->side == 'B') {
            auto it = asks.begin();
            while (it != asks.end() && taker->remaining() > 0) {
                if (taker->type != 'M' && it->first > taker->price) break;
                auto& level = it->second;
                auto level_it = level.begin();
                while (level_it != level.end() && taker->remaining() > 0) {
                    Order* m = *level_it;
                    long long fillQty = min(m->remaining(), taker->remaining());
                    m->filled += fillQty;
                    taker->filled += fillQty;
                    fills.push_back({m->id, taker->id, m->price, fillQty});
                    if (m->remaining() == 0) {
                        orders.erase(m->id);
                        level_it = level.erase(level_it);
                    } else {
                        ++level_it;
                    }
                }
                if (level.empty()) it = asks.erase(it);
                else ++it;
            }
        } else {
            auto it = bids.begin();
            while (it != bids.end() && taker->remaining() > 0) {
                if (taker->type != 'M' && it->first < taker->price) break;
                auto& level = it->second;
                auto level_it = level.begin();
                while (level_it != level.end() && taker->remaining() > 0) {
                    Order* m = *level_it;
                    long long fillQty = min(m->remaining(), taker->remaining());
                    m->filled += fillQty;
                    taker->filled += fillQty;
                    fills.push_back({m->id, taker->id, m->price, fillQty});
                    if (m->remaining() == 0) {
                        orders.erase(m->id);
                        level_it = level.erase(level_it);
                    } else {
                        ++level_it;
                    }
                }
                if (level.empty()) it = bids.erase(it);
                else ++it;
            }
        }
        return fills;
    }

    vector<Fill> add(const string& id, char side, char type, long long price, long long qty) {
        if (orders.find(id) != orders.end()) return {};
        seq++;
        pool.push_back({id, side, type, price, qty, 0, seq});
        Order* o = &pool.back();
        orders[id] = o;
        vector<Fill> fills = match(o);

        if (o->remaining() > 0) {
            if (type == 'L') {
                if (side == 'B') bids[price].push_back(o);
                else asks[price].push_back(o);
            } else {
                orders.erase(id);
            }
        } else {
            orders.erase(id);
        }
        return fills;
    }

    bool cancel(const string& id) {
        auto it = orders.find(id);
        if (it == orders.end()) return false;
        Order* o = it->second;
        orders.erase(it);

        if (o->side == 'B') {
            auto level_it = bids.find(o->price);
            if (level_it != bids.end()) {
                auto& level = level_it->second;
                level.erase(remove(level.begin(), level.end(), o), level.end());
                if (level.empty()) bids.erase(level_it);
            }
        } else {
            auto level_it = asks.find(o->price);
            if (level_it != asks.end()) {
                auto& level = level_it->second;
                level.erase(remove(level.begin(), level.end(), o), level.end());
                if (level.empty()) asks.erase(level_it);
            }
        }
        return true;
    }

    ~Book() {}
};

int main() {
    uWS::App().ws<Book>("/*", {
        .open = [](auto *ws) {
            new (ws->getUserData()) Book();
        },
        .message = [](auto *ws, std::string_view message, uWS::OpCode opCode) {
            Book* book = (Book*)ws->getUserData();
            string_view cmd, id, sideStr, typeStr, priceStr;
            
            auto next_token = [&](string_view& str, string_view& token) -> bool {
                size_t start = str.find_first_not_of(' ');
                if (start == string_view::npos) return false;
                size_t end = str.find_first_of(' ', start);
                if (end == string_view::npos) {
                    token = str.substr(start);
                    str = "";
                } else {
                    token = str.substr(start, end - start);
                    str = str.substr(end);
                }
                return true;
            };

            string_view str = message;
            if (!next_token(str, cmd)) return;

            auto format_int = [](long long v, char* buf, int& len) {
                if (v == 0) { buf[len++] = '0'; return; }
                char tmp[32]; int tlen = 0;
                while(v > 0) { tmp[tlen++] = '0' + (v % 10); v /= 10; }
                for(int i = tlen - 1; i >= 0; i--) buf[len++] = tmp[i];
            };

            if (cmd == "ADD") {
                string_view qtyStr;
                if (!next_token(str, id) || !next_token(str, sideStr) || 
                    !next_token(str, typeStr) || !next_token(str, priceStr) || 
                    !next_token(str, qtyStr)) {
                    string sid(id);
                    char rejbuf[128];
                    int rejlen = snprintf(rejbuf, sizeof(rejbuf), "REJ %s bad_format", sid.c_str());
                    ws->send(string_view(rejbuf, rejlen), opCode);
                    return;
                }
                char side = sideStr[0];
                char type = typeStr[0];
                long long price = parsePrice(priceStr);
                
                long long qty = 0;
                for (char c : qtyStr) qty = qty * 10 + (c - '0');

                string sid(id);
                vector<Fill> fills = book->add(sid, side, type, price, qty);
                for (const auto& f : fills) {
                    char buf[256];
                    int len = 0;
                    auto add_str = [&](string_view s) { memcpy(buf + len, s.data(), s.length()); len += s.length(); };
                    add_str("FILL "); add_str(id); buf[len++] = ' ';
                    add_str(f.maker); buf[len++] = ' ';
                    add_str(f.taker); buf[len++] = ' ';
                    format_int(f.price / 10000, buf, len);
                    buf[len++] = '.';
                    long long fr = f.price % 10000;
                    buf[len++] = '0' + (fr / 1000); fr %= 1000;
                    buf[len++] = '0' + (fr / 100); fr %= 100;
                    buf[len++] = '0' + (fr / 10); fr %= 10;
                    buf[len++] = '0' + fr;
                    buf[len++] = ' ';
                    format_int(f.qty, buf, len);
                    ws->send(string_view(buf, len), opCode);
                }
                char ackbuf[128];
                int acklen = 0;
                memcpy(ackbuf, "ACK ", 4); acklen += 4;
                memcpy(ackbuf + acklen, id.data(), id.length()); acklen += id.length();
                ws->send(string_view(ackbuf, acklen), opCode);
            } else if (cmd == "CAN") {
                if (!next_token(str, id)) return;
                string sid(id);
                if (book->cancel(sid)) {
                    char ackbuf[128];
                    int acklen = 0;
                    memcpy(ackbuf, "ACK ", 4); acklen += 4;
                    memcpy(ackbuf + acklen, id.data(), id.length()); acklen += id.length();
                    ws->send(string_view(ackbuf, acklen), opCode);
                } else {
                    char rejbuf[128];
                    int rejlen = 0;
                    memcpy(rejbuf, "REJ ", 4); rejlen += 4;
                    memcpy(rejbuf + rejlen, id.data(), id.length()); rejlen += id.length();
                    memcpy(rejbuf + rejlen, " not_found", 10); rejlen += 10;
                    ws->send(string_view(rejbuf, rejlen), opCode);
                }
            }
        },
        .close = [](auto *ws, int /*code*/, std::string_view /*message*/) {
            // uWebSockets automatically destructs UserData, so do NOT call book->~Book()
        }
    }).listen(8080, [](auto *listen_socket) {
        if (listen_socket) cout << "Listening on port 8080" << endl;
    }).run();
}

// Generated by CoffeeScript 1.8.0
(function() {
  var app;

  app = angular.module('DoodleApp', ['websocket']);

  app.value('wsPort', 1111);

  app.controller('DoodleCtrl', function($scope, $timeout, $websocket, wsPort) {
    var ws, wsProto;
    wsProto = "https:" === document.location.protocol ? "wss" : "ws";
    ws = $websocket.connect("" + wsProto + "://" + location.hostname + ":" + wsPort + "/ws");
    ws.register('', function(topic, body) {
      return console.log('mqtt:', topic, body);
    });
    ws.emit('/doodle/dah', [7, 8, 9]);
    $timeout(function() {
      return ws.emit('/doodle', [1, 2, 3]);
    }, 1000);
    return $timeout(function() {
      return ws.emit('/doodledah', [4, 5, 6]);
    }, 2000);
  });

}).call(this);


//# sourceMappingURL=doodle.js.map
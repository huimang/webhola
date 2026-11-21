import com.sun.net.httpserver.HttpServer;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import org.springframework.web.bind.annotation.RequestMethod;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.servlet.mvc.method.RequestMappingInfo;
import org.springframework.web.servlet.mvc.method.annotation.RequestMappingHandlerMapping;

// Small real Spring registry + JVM fixture; no application credentials or data.
public class ArthasFixture {
    public static final RequestMappingHandlerMapping mapping = new RequestMappingHandlerMapping();
    public static final Controller controller = new Controller();
    public static class Service {
        public String greet(String name) { return "hello " + name; }
        public String greet(int count) { return "count " + count; }
    }
    public static class Controller {
        final Service service = new Service();
        public String hello(@RequestParam(name = "userName") String name) { return service.greet(name); }
    }
    public static void main(String[] args) throws Exception {
        mapping.registerMapping(RequestMappingInfo.paths("/hello/{name}").methods(RequestMethod.GET).build(),
                controller, Controller.class.getMethod("hello", String.class));
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/hello/", e -> {
            byte[] body = controller.hello(e.getRequestURI().getPath().substring(7)).getBytes(StandardCharsets.UTF_8);
            e.sendResponseHeaders(200, body.length);
            e.getResponseBody().write(body);
            e.close();
        });
        server.start();
        System.out.println(server.getAddress().getPort());
        System.out.flush();
    }
}
